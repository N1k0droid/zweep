// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.service

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import net.nicodroid.zweep.core.Backoff
import net.nicodroid.zweep.core.Incoming
import net.nicodroid.zweep.core.parseFrame
import net.nicodroid.zweep.net.Http
import net.nicodroid.zweep.net.Link
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener

enum class ConnState { STOPPED, CONNECTING, CONNECTED, WAITING, REJECTED }

/**
 * The /v1/stream WebSocket of one server. Frames are processed one at a time, in order, by
 * [handler]; the connection reconnects with backoff and rotates the service URLs of the server.
 */
class Connection(
    private val serverRef: Long,
    private val scope: CoroutineScope,
    private val links: () -> List<Link>,
    private val token: () -> String?,
    private val handler: suspend (Connection, Incoming) -> Unit,
    private val onOpen: suspend (Connection) -> Unit,
    private val onState: (Long, ConnState, String?) -> Unit,
) {
    @Volatile var state = ConnState.STOPPED
        private set

    private val frames = Channel<Incoming>(Channel.UNLIMITED)
    private val backoff = Backoff()
    private var socket: WebSocket? = null
    private var worker: Job? = null
    private var retry: Job? = null
    private var urlIndex = 0
    private var generation = 0

    @Volatile private var lastPong = 0L

    @Volatile private var pingSentAt = 0L

    fun start() {
        if (worker == null) {
            worker = scope.launch { for (f in frames) runCatching { handler(this@Connection, f) }.onFailure { Engine.warn("frame", it) } }
        }
        if (state == ConnState.STOPPED || state == ConnState.WAITING) connect()
    }

    fun stop() {
        retry?.cancel()
        generation++
        socket?.close(1000, null)
        socket = null
        set(ConnState.STOPPED, null)
    }

    /** The token was revoked or the server identity changed: stop until the user acts */
    fun reject(reason: String) {
        stop()
        set(ConnState.REJECTED, reason)
    }

    /** Network available again: reconnect now instead of waiting for the backoff */
    fun kick() {
        if (state == ConnState.WAITING) {
            retry?.cancel()
            backoff.reset()
            connect()
        }
    }

    fun send(text: String): Boolean = socket?.send(text) ?: false

    /** Aligned keepalive (all servers at once); a missing pong means a half-open connection */
    fun ping(nowMs: Long) {
        if (state != ConnState.CONNECTED) return
        if (pingSentAt > 0 && lastPong < pingSentAt && nowMs - pingSentAt > PONG_TIMEOUT_MS) {
            fail("no pong")
            return
        }
        pingSentAt = nowMs
        send("{\"type\":\"ping\",\"clock\":$nowMs}")
    }

    private fun connect() {
        val all = links()
        val t = token()
        if (all.isEmpty() || t == null) {
            set(ConnState.REJECTED, "missing configuration")
            return
        }
        val link = all[urlIndex % all.size]
        val gen = ++generation
        set(ConnState.CONNECTING, link.baseUrl)
        val url = link.baseUrl.trimEnd('/').replaceFirst("http", "ws") + "/v1/stream"
        val client = runCatching { Http.client(link) }.getOrElse {
            set(ConnState.REJECTED, it.message)
            return
        }
        val req = Request.Builder().url(url).header("Authorization", "Bearer $t").header("User-Agent", "Zweep-Android").build()
        socket = client.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                if (gen != generation) return
                backoff.reset()
                lastPong = System.currentTimeMillis()
                pingSentAt = 0
                set(ConnState.CONNECTED, link.baseUrl)
                scope.launch { runCatching { onOpen(this@Connection) }.onFailure { Engine.warn("open", it) } }
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                if (gen != generation) return
                val f = parseFrame(text)
                if (f is Incoming.Pong) lastPong = System.currentTimeMillis() else frames.trySend(f)
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(1000, null)
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                if (gen == generation) scheduleRetry(null)
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                if (gen != generation) return
                if (response?.code == 401 || response?.code == 403) {
                    set(ConnState.REJECTED, "unauthorized")
                    return
                }
                urlIndex++ // next service URL of the same server
                scheduleRetry(t.message)
            }
        })
    }

    private fun fail(why: String) {
        generation++
        socket?.cancel()
        socket = null
        urlIndex++
        scheduleRetry(why)
    }

    private fun scheduleRetry(why: String?) {
        if (state == ConnState.STOPPED || state == ConnState.REJECTED) return
        socket = null
        set(ConnState.WAITING, why)
        val wait = backoff.next()
        retry?.cancel()
        retry = scope.launch {
            delay(wait)
            connect()
        }
    }

    private fun set(s: ConnState, detail: String?) {
        state = s
        onState(serverRef, s, detail)
    }

    companion object {
        const val PONG_TIMEOUT_MS = 20_000L
    }
}
