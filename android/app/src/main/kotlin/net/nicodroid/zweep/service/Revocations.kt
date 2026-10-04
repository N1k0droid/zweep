// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.service

import android.util.Base64
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import net.nicodroid.zweep.core.WireJson
import net.nicodroid.zweep.data.Vault
import net.nicodroid.zweep.net.Api
import net.nicodroid.zweep.net.ApiException
import net.nicodroid.zweep.net.Link

/**
 * Logout must revoke the device token on the server even when the server is unreachable at that
 * moment. A failed revocation is queued (the token stays sealed by the Keystore) and retried at app
 * start, by the watchdog and at the next logout, until the server confirms or the token is refused.
 */
object Revocations {
    @Serializable
    data class Pending(val urls: List<String>, val pins: List<String>, val cleartext: Boolean, val token: String, val since: Long)

    private const val GIVE_UP_MS = 30 * 86_400_000L
    private val lock = Mutex()
    private val serializer = ListSerializer(Pending.serializer())

    suspend fun add(links: List<Link>, tokenEnc: ByteArray) = lock.withLock {
        val l = links.first()
        save(load() + Pending(links.map { it.baseUrl }, l.pins, l.cleartextAccepted, Base64.encodeToString(tokenEnc, Base64.NO_WRAP), System.currentTimeMillis()))
    }

    /** Tries every queued revocation; returns true while some are still pending */
    suspend fun retry(): Boolean = withContext(Dispatchers.IO) { lock.withLock { retryLocked() } }

    private suspend fun retryLocked(): Boolean {
        val all = load()
        if (all.isEmpty()) return false
        val now = System.currentTimeMillis()
        val left = all.filter { now - it.since < GIVE_UP_MS && !revoke(it) }
        if (left.size != all.size) save(left)
        return left.isNotEmpty()
    }

    private suspend fun revoke(p: Pending): Boolean {
        val token = runCatching { Vault.open(Base64.decode(p.token, Base64.NO_WRAP)) }.getOrElse { return true } // key lost: nothing to send
        for (url in p.urls) {
            try {
                Api(Link(url, p.pins, p.cleartext), token).logout()
                return true
            } catch (e: ApiException) {
                if (e.status == 401 || e.status == 403 || e.status == 404) return true // already revoked or unknown
            } catch (e: Exception) {
                Engine.warn("revocation", e)
            }
        }
        return false
    }

    private suspend fun load(): List<Pending> =
        Engine.settings.pendingRevocations()?.let { runCatching { WireJson.decodeFromString(serializer, it) }.getOrNull() }.orEmpty()

    private suspend fun save(l: List<Pending>) = Engine.settings.setPendingRevocations(WireJson.encodeToString(serializer, l))
}
