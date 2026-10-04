// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.core

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put

/** Wire format of /v1/stream and of the app REST API (server: internal/delivery, internal/api). */
val WireJson = Json {
    ignoreUnknownKeys = true
    coerceInputValues = true // a JSON null for a field with a default (e.g. an empty Go slice) takes the default
    explicitNulls = false
    encodeDefaults = true
}

@Serializable
data class MsgFrame(
    val id: String,
    val seq: Long,
    val sid: String,
    val ver: Long,
    val kind: String,
    val sev: Int,
    val channels: List<String> = emptyList(),
    val source: String = "",
    val ts: Long = 0,
    val title: String = "",
    val body: JsonObject = JsonObject(emptyMap()),
)

@Serializable
data class Welcome(
    @SerialName("start_seq") val startSeq: Long,
    @SerialName("head_seq") val headSeq: Long = 0,
    @SerialName("oldest_seq") val oldestSeq: Long = 0,
    @SerialName("server_time") val serverTime: Long = 0,
    @SerialName("proj_rev") val projRev: Long = 0,
    val features: List<String> = emptyList(),
    @SerialName("node_id") val nodeId: String = "",
    @SerialName("server_id") val serverId: String = "",
)

@Serializable
data class Gap(@SerialName("from_seq") val fromSeq: Long, @SerialName("to_seq") val toSeq: Long)

@Serializable
data class Notice(val code: String, val message: String = "")

@Serializable
data class Host(val hostid: String = "", val host: String = "", val name: String = "")

@Serializable
data class Tag(val tag: String = "", val value: String = "")

@Serializable
data class ProblemRow(
    val source: String,
    val eventid: String,
    val status: String,
    val name: String,
    val severity: Int,
    val clock: String,
    @SerialName("r_clock") val rClock: String? = null,
    val acknowledged: Boolean = false,
    val suppressed: Boolean = false,
    val hosts: List<Host> = emptyList(),
    val hostgroups: List<String> = emptyList(),
    val tags: List<Tag> = emptyList(),
    val version: Long = 0,
) {
    /** In the Zabbix problem list right now (not resolved, not gone) */
    val isOpen: Boolean get() = status == "open" || status == "acknowledged" || status == "suppressed"
    val isResolved: Boolean get() = status == "resolved"
}

@Serializable
data class ProblemKey(val source: String, val eventid: String)

@Serializable
data class ProblemsDelta(
    @SerialName("from_rev") val fromRev: Long,
    @SerialName("to_rev") val toRev: Long,
    val upsert: List<ProblemRow> = emptyList(),
    val remove: List<ProblemKey> = emptyList(),
)

@Serializable
data class ProblemsStale(val source: String, val stale: Boolean)

@Serializable
data class AckResult(
    @SerialName("request_id") val requestId: String,
    val state: String,
    val source: String = "",
    val eventid: JsonElement? = null, // string, or a number from older servers
    val reason: String? = null,
)

/** A frame received on /v1/stream */
sealed interface Incoming {
    data class Msg(val msg: MsgFrame) : Incoming
    data class Hello(val welcome: Welcome) : Incoming
    data class Hole(val gap: Gap) : Incoming
    data class Note(val notice: Notice) : Incoming
    data class Delta(val delta: ProblemsDelta) : Incoming
    data class Stale(val stale: ProblemsStale) : Incoming
    data class AckDone(val result: AckResult) : Incoming
    data object Pong : Incoming
    data class Unknown(val type: String) : Incoming
}

/** Parses one text frame; malformed frames become Unknown and are ignored by the caller */
fun parseFrame(text: String): Incoming {
    val obj = runCatching { WireJson.parseToJsonElement(text).jsonObject }.getOrNull() ?: return Incoming.Unknown("")
    val type = runCatching { obj["type"]?.jsonPrimitive?.content }.getOrNull() ?: return Incoming.Unknown("")
    return runCatching {
        when (type) {
            "msg" -> Incoming.Msg(WireJson.decodeFromJsonElement(MsgFrame.serializer(), obj))
            "welcome" -> Incoming.Hello(WireJson.decodeFromJsonElement(Welcome.serializer(), obj))
            "gap" -> Incoming.Hole(WireJson.decodeFromJsonElement(Gap.serializer(), obj))
            "notice" -> Incoming.Note(WireJson.decodeFromJsonElement(Notice.serializer(), obj))
            "problems.delta" -> Incoming.Delta(WireJson.decodeFromJsonElement(ProblemsDelta.serializer(), obj))
            "problems.stale" -> Incoming.Stale(WireJson.decodeFromJsonElement(ProblemsStale.serializer(), obj))
            "ack.result" -> Incoming.AckDone(WireJson.decodeFromJsonElement(AckResult.serializer(), obj))
            "pong" -> Incoming.Pong
            else -> Incoming.Unknown(type)
        }
    }.getOrElse { Incoming.Unknown(type) }
}

/** Receipt states understood by the server */
object ReceiptState {
    const val DELIVERED = "delivered"
    const val SHOWN = "shown"
    const val NOT_SHOWN = "not_shown"
    const val FILTERED = "filtered"
}

data class DeviceInfo(val name: String, val appVersion: String, val osVersion: String, val vendor: String, val model: String, val appBuild: Long = 0)

object Frames {
    fun hello(ackedSeq: Long, projRev: Long, device: DeviceInfo, perms: JsonElement?, dndUntil: Long?, clock: Long): String =
        buildJsonObject {
            put("type", "hello")
            put("acked_seq", ackedSeq)
            put("proj_rev", projRev)
            put("proj_resolved", true) // the list keeps resolved problems (Recent view)
            put("clock", clock)
            put("app_version", device.appVersion)
            put("device", deviceJson(device))
            if (perms != null) put("perms", perms)
            if (dndUntil != null) put("dnd_until", dndUntil)
        }.toString()

    fun ack(id: String, seq: Long, state: String, reason: String?, ackedSeq: Long): String = buildJsonObject {
        put("type", "ack")
        put("id", id)
        put("seq", seq)
        put("state", state)
        if (reason != null) put("reason", reason)
        put("acked_seq", ackedSeq)
    }.toString()

    fun resync(fromSeq: Long): String = buildJsonObject {
        put("type", "resync")
        put("from_seq", fromSeq)
    }.toString()

    fun status(device: DeviceInfo, perms: JsonElement?, dndUntil: Long?): String = buildJsonObject {
        put("type", "status")
        put("device", deviceJson(device))
        if (perms != null) put("perms", perms)
        put("dnd_until", dndUntil ?: 0L)
    }.toString()

    fun bye(reason: String): String = buildJsonObject {
        put("type", "bye")
        put("reason", reason)
    }.toString()

    private fun deviceJson(d: DeviceInfo) = buildJsonObject {
        put("name", d.name)
        put("platform", "android")
        put("app_version", d.appVersion)
        put("os_version", d.osVersion)
        put("vendor", d.vendor)
        put("model", d.model)
        if (d.appBuild > 0) put("app_build", d.appBuild)
    }
}
