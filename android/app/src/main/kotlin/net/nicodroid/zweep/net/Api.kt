// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.net

import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.serialization.KSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.builtins.serializer
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonPrimitive
import net.nicodroid.zweep.core.DeviceInfo
import net.nicodroid.zweep.core.ProblemRow
import net.nicodroid.zweep.core.WireJson
import okhttp3.Call
import okhttp3.Callback
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import java.io.IOException
import java.security.KeyStore
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLSession
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.HostnameVerifier
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509TrustManager
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

/** How the app reaches one Zweep server */
data class Link(val baseUrl: String, val pins: List<String>, val cleartextAccepted: Boolean) {
    val cleartext get() = baseUrl.startsWith("http://")
}

class ApiException(val status: Int, val code: String, message: String) : IOException(message)

/** SPKI SHA-256 of a certificate, base64 (the pin format of the QR code) */
fun spkiPin(cert: X509Certificate): String =
    java.util.Base64.getEncoder().encodeToString(MessageDigest.getInstance("SHA-256").digest(cert.publicKey.encoded))

/** Position in the chain of the first certificate whose key is pinned, -1 when none */
internal fun pinnedIndex(chain: Array<out X509Certificate>, pins: Set<String>): Int = chain.indexOfFirst { spkiPin(it) in pins }

/**
 * With pins, the server is trusted if its certificate key, or the key of a CA that signed it, matches
 * one of them (self-signed or company CA); without pins, the system trust store decides
 * (Certificate Transparency on Android 17+ included). Every certificate up to the pinned one must be
 * signed by the next and valid: a pinned CA appended to an unrelated chain does not pass.
 */
internal class PinTrustManager(private val pins: Set<String>) : X509TrustManager {
    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) = throw CertificateException("client certificates are not used")

    override fun checkServerTrusted(chain: Array<out X509Certificate>, authType: String) {
        val k = pinnedIndex(chain, pins)
        if (k < 0) throw CertificateException("server key does not match the configured pin")
        for (i in 0..k) chain[i].checkValidity()
        for (i in 0 until k) {
            try {
                chain[i].verify(chain[i + 1].publicKey)
            } catch (e: Exception) {
                throw CertificateException("certificate chain not signed by the pinned key", e)
            }
        }
    }

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}

/**
 * Host name check with pins: a pinned server key identifies the server by itself (IP or internal
 * name); a pinned CA signs many servers, so the name must match like with the system trust store.
 */
internal class PinHostnameVerifier(private val pins: Set<String>, private val fallback: HostnameVerifier) : HostnameVerifier {
    override fun verify(hostname: String, session: SSLSession): Boolean {
        val chain = runCatching { session.peerCertificates.filterIsInstance<X509Certificate>().toTypedArray() }.getOrNull() ?: return false
        return pinnedIndex(chain, pins) == 0 || fallback.verify(hostname, session)
    }
}

object Http {
    private val base: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(10, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.SECONDS)
        .writeTimeout(15, TimeUnit.SECONDS)
        .pingInterval(0, TimeUnit.SECONDS) // keepalive is aligned across servers by the service
        .retryOnConnectionFailure(true)
        .build()

    fun client(link: Link): OkHttpClient {
        if (link.cleartext && !link.cleartextAccepted) throw IOException("unencrypted connection not confirmed")
        if (link.pins.isEmpty() || link.cleartext) return base
        val tm = PinTrustManager(link.pins.toSet())
        val ctx = SSLContext.getInstance("TLS").apply { init(null, arrayOf(tm), null) }
        return base.newBuilder()
            .sslSocketFactory(ctx.socketFactory, tm)
            .hostnameVerifier(PinHostnameVerifier(link.pins.toSet(), HttpsURLConnection.getDefaultHostnameVerifier()))
            .build()
    }

    /** Default trust manager, used to tell a normal HTTPS server from one that needs a pin */
    fun systemTrusts(chain: Array<X509Certificate>): Boolean = runCatching {
        val f = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm()).apply { init(null as KeyStore?) }
        (f.trustManagers.first() as X509TrustManager).checkServerTrusted(chain, "RSA")
    }.isSuccess
}

suspend fun Call.await(): Response = suspendCancellableCoroutine { cont ->
    enqueue(object : Callback {
        override fun onFailure(call: Call, e: IOException) {
            if (!cont.isCancelled) cont.resumeWithException(e)
        }

        override fun onResponse(call: Call, response: Response) = cont.resume(response)
    })
    cont.invokeOnCancellation { runCatching { cancel() } }
}

@Serializable
data class EnrollResponse(
    val token: String,
    @SerialName("device_id") val deviceId: String,
    val username: String,
    @SerialName("acked_seq") val ackedSeq: Long = 0,
    @SerialName("server_id") val serverId: String,
    @SerialName("node_id") val nodeId: String = "",
    @SerialName("service_urls") val serviceUrls: List<String> = emptyList(),
)

@Serializable
data class ConfigSource(val id: String, val name: String, @SerialName("frontend_url") val frontendUrl: String = "", @SerialName("api_mode") val apiMode: String = "disabled")

@Serializable
data class ConfigChannel(val id: String, val kind: String, val name: String, val enabled: Boolean = true, val color: String = "")

@Serializable
data class ConfigResponse(
    @SerialName("server_id") val serverId: String,
    @SerialName("node_id") val nodeId: String = "",
    @SerialName("service_urls") val serviceUrls: List<String> = emptyList(),
    val username: String,
    val sources: List<ConfigSource> = emptyList(),
    val channels: List<ConfigChannel> = emptyList(),
    val hostgroups: List<String> = emptyList(),
    val features: List<String> = emptyList(),
    @SerialName("keepalive_s") val keepaliveS: Long = 60,
    @SerialName("app_update") val appUpdate: AppUpdate? = null,
)

/** The newest APK the server offers (its directory); compared with BuildConfig.VERSION_CODE */
@Serializable
data class AppUpdate(
    @SerialName("version_code") val versionCode: Long,
    @SerialName("version_name") val versionName: String,
    val size: Long = 0,
    val sha256: String,
    val path: String = "/v1/app/update/apk",
)

@Serializable
data class SourceStateJson(val id: String, @SerialName("data_as_of") val dataAsOf: String? = null, val stale: Boolean = false)

@Serializable
data class Snapshot(val rev: Long, val stale: Boolean = false, val sources: List<SourceStateJson> = emptyList(), val checksum: String = "", val problems: List<ProblemRow> = emptyList())

/** The History view: problems of a period read from Zabbix, resolved or not, newest first */
@Serializable
data class ProblemHistory(val period: Int = 0, val truncated: Boolean = false, val sources: List<SourceStateJson> = emptyList(), val problems: List<ProblemRow> = emptyList())

@Serializable
data class HistoryEntry(
    val clock: String,
    @SerialName("author_kind") val authorKind: String,
    @SerialName("author_name") val authorName: String? = null,
    @SerialName("via_service_user") val viaServiceUser: Boolean = false,
    val actions: List<String> = emptyList(),
    val message: String? = null,
    @SerialName("old_severity") val oldSeverity: Int? = null,
    @SerialName("new_severity") val newSeverity: Int? = null,
)

@Serializable
data class Detail(val problem: ProblemRow, val history: List<HistoryEntry> = emptyList(), @SerialName("data_as_of") val dataAsOf: String? = null, @SerialName("frontend_url") val frontendUrl: String? = null)

@Serializable
data class RotateResponse(val token: String)

@Serializable
data class AckState(val state: String, val reason: String? = null)

/** REST calls of one server; the token is sent only as a Bearer header, never in URLs */
class Api(private val link: Link, private val token: String?) {
    private val json = "application/json".toMediaType()
    private val client get() = Http.client(link)

    private fun url(path: String) = (link.baseUrl.trimEnd('/') + path).toHttpUrlOrNull() ?: throw IOException("invalid server URL")

    private suspend fun <T> call(method: String, path: String, body: String?, ser: KSerializer<T>?): T? {
        val b = Request.Builder().url(url(path)).header("User-Agent", "Zweep-Android")
        token?.let { b.header("Authorization", "Bearer $it") }
        b.method(method, body?.toRequestBody(json) ?: if (method == "POST") "{}".toRequestBody(json) else null)
        client.newCall(b.build()).await().use { r ->
            val text = r.body.string()
            if (!r.isSuccessful) {
                val code = runCatching { WireJson.parseToJsonElement(text).let { (it as JsonObject)["error"]?.jsonPrimitive?.content } }.getOrNull()
                throw ApiException(r.code, code ?: "http_${r.code}", "HTTP ${r.code}${code?.let { " ($it)" } ?: ""}")
            }
            return ser?.let { WireJson.decodeFromString(it, text) }
        }
    }

    suspend fun enrollWithCode(code: String, device: DeviceInfo) =
        call("POST", "/v1/app/enroll", enrollBody("\"code\":${quote(code)}", device), EnrollResponse.serializer())!!

    suspend fun enrollWithPassword(username: String, password: String, device: DeviceInfo) =
        call("POST", "/v1/app/enroll", enrollBody("\"username\":${quote(username)},\"password\":${quote(password)}", device), EnrollResponse.serializer())!!

    suspend fun config() = call("GET", "/v1/app/config", null, ConfigResponse.serializer())!!
    /** resolvedSeconds > 0: also the problems resolved in that window (servers with "problem_views") */
    suspend fun snapshot(resolvedSeconds: Int = 0) =
        call("GET", "/v1/app/problems" + (if (resolvedSeconds > 0) "?resolved=$resolvedSeconds" else ""), null, Snapshot.serializer())!!
    suspend fun history(periodSeconds: Int) = call("GET", "/v1/app/problems/history?period=$periodSeconds", null, ProblemHistory.serializer())!!
    suspend fun detail(source: String, eventid: String) = call("GET", "/v1/app/problems/${enc(source)}/${enc(eventid)}", null, Detail.serializer())!!

    suspend fun ack(requestId: String, source: String, eventid: String, text: String) = call(
        "POST", "/v1/app/acks",
        "{\"request_id\":${quote(requestId)},\"source\":${quote(source)},\"eventid\":${quote(eventid)},\"text\":${quote(text)}}",
        AckState.serializer(),
    )!!

    suspend fun ackState(requestId: String) = call("GET", "/v1/app/acks/${enc(requestId)}", null, AckState.serializer())!!

    /** Forced close of an alert for every recipient; needs the "close" feature */
    suspend fun closeAlert(sid: String) {
        call<Unit>("POST", "/v1/app/alerts/close", "{\"sid\":${quote(sid)}}", null)
    }

    suspend fun rotate(): String = call("POST", "/v1/app/token/rotate", null, RotateResponse.serializer())!!.token

    suspend fun logout() {
        call<Unit>("POST", "/v1/app/logout", null, null)
    }

    suspend fun testAlarm(severity: Int) {
        call<Unit>("POST", "/v1/app/test", "{\"severity\":$severity}", null)
    }

    /** Downloads a file on this authenticated connection; returns the SHA-256 (hex) of what was written */
    suspend fun download(path: String, dest: java.io.File, progress: (Long, Long) -> Unit): String {
        val b = Request.Builder().url(url(path)).header("User-Agent", "Zweep-Android")
        token?.let { b.header("Authorization", "Bearer $it") }
        client.newCall(b.get().build()).await().use { r ->
            if (!r.isSuccessful) throw ApiException(r.code, "http_${r.code}", "HTTP ${r.code}")
            val total = r.body.contentLength()
            val md = java.security.MessageDigest.getInstance("SHA-256")
            var done = 0L
            var last = -1L
            dest.outputStream().use { out ->
                r.body.byteStream().use { input ->
                    val buf = ByteArray(64 * 1024)
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        out.write(buf, 0, n)
                        md.update(buf, 0, n)
                        done += n
                        if (done - last >= 256 * 1024) {
                            last = done
                            progress(done, total)
                        }
                    }
                }
            }
            progress(done, total)
            return md.digest().joinToString("") { "%02x".format(it) }
        }
    }

    private fun enrollBody(creds: String, d: DeviceInfo) =
        "{$creds,\"device\":{\"name\":${quote(d.name)},\"platform\":\"android\",\"app_version\":${quote(d.appVersion)}," +
            "\"os_version\":${quote(d.osVersion)},\"vendor\":${quote(d.vendor)},\"model\":${quote(d.model)},\"app_build\":${d.appBuild}}}"

    private fun quote(s: String) = WireJson.encodeToString(String.serializer(), s)
    private fun enc(s: String) = java.net.URLEncoder.encode(s, "UTF-8").replace("+", "%20")
}
