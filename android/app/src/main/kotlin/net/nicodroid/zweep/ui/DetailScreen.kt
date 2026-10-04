// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.ui

import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.browser.customtabs.CustomTabsIntent
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import net.nicodroid.zweep.R
import net.nicodroid.zweep.core.normalizeAckText
import net.nicodroid.zweep.data.AckEntity
import net.nicodroid.zweep.data.MessageEntity
import net.nicodroid.zweep.data.SourceEntity
import net.nicodroid.zweep.net.Detail
import net.nicodroid.zweep.net.HistoryEntry
import net.nicodroid.zweep.service.Notifier

@Composable
fun DetailScreen(vm: AppViewModel, r: Route.Detail, onBack: () -> Unit) {
    val ctx = LocalContext.current
    var local by remember { mutableStateOf<List<MessageEntity>>(emptyList()) }
    var detail by remember { mutableStateOf<Detail?>(null) }
    var detailError by remember { mutableStateOf(false) }
    var source by remember { mutableStateOf<SourceEntity?>(null) }
    val acks by remember(r) { vm.acksFor(r.serverRef, r.source, r.eventId) }.collectAsState(emptyList())
    val confirmed = acks.count { it.state == AckEntity.CONFIRMED }
    LaunchedEffect(r, confirmed) { // an ack confirmed in Zabbix changes status and history: reload
        r.sid?.let { sid ->
            vm.markRead(r.serverRef, sid)
            vm.history(r.serverRef, sid) { local = it }
        }
        source = vm.source(r.serverRef, r.source)
        if (source?.apiMode != null && source?.apiMode != "disabled") {
            vm.detail(r.serverRef, r.source, r.eventId).onSuccess { detail = it }.onFailure { detailError = true }
        }
    }
    val p = detail?.problem
    val first = local.firstOrNull()
    val last = local.lastOrNull()
    val sev = p?.severity ?: local.maxOfOrNull { it.sev } ?: 0
    // A forced close resolves the alert even when Zabbix still lists the problem
    val closed = last?.takeIf { it.kind == "recovery" }?.let { closedInfo(it.body) }
    val status = when {
        closed != null -> "resolved"
        p != null -> p.status
        last?.kind == "recovery" -> "resolved"
        else -> "open"
    }
    val channels by vm.channels.collectAsStateWithLifecycle()
    val custom = last?.channels?.split(',')?.firstNotNullOfOrNull { id -> channels.firstOrNull { it.serverRef == r.serverRef && it.id == id && it.kind == "custom" } }
    Column(Modifier.fillMaxSize()) {
        Header((custom?.name ?: stringResource(Notifier.severityLabel(sev))) + " · " + statusText(status), onBack)
        Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Text(p?.name ?: first?.name?.ifEmpty { first.title } ?: "", color = Zw.textPrimary, fontSize = 20.sp, fontWeight = FontWeight.Bold)
            if (custom != null) ChannelBadge(custom.name, custom.color) else SeverityBadge(sev)
            Field(R.string.detail_host, p?.hosts?.joinToString { it.name.ifEmpty { it.host } } ?: first?.host ?: "")
            if (!p?.hostgroups.isNullOrEmpty()) Field(R.string.detail_groups, p!!.hostgroups.joinToString())
            val start = p?.clock?.let { isoMs(it) } ?: first?.eventTime ?: 0
            if (start > 0) Field(R.string.detail_start, timeShort(start) + "  (" + duration(start) + ")")
            if (!p?.tags.isNullOrEmpty()) Field(R.string.detail_tags, p!!.tags.joinToString("  ") { if (it.value.isEmpty()) it.tag else "${it.tag}:${it.value}" })
            Field(R.string.detail_source, source?.name ?: first?.sourceName ?: r.source)
            // In a custom channel the severity is a detail of the event (absent for internal events)
            if (custom != null && sev >= 0) Field(R.string.detail_severity, stringResource(Notifier.severityLabel(sev)), Zw.severity(sev))
            // Text of a dashboard announcement: the start, and the closing message if any
            local.mapNotNull { net.nicodroid.zweep.service.bodyText(it.body) }.distinct().forEach { Field(R.string.detail_message, it) }
            if (closed != null) Field(R.string.detail_closed,
                if (closed.second == "orphan") stringResource(R.string.detail_closed_orphan) else stringResource(R.string.detail_closed_manual, closed.first))
            if (detail?.dataAsOf != null) Text(stringResource(R.string.detail_data_as_of, timeShort(isoMs(detail?.dataAsOf))), color = Zw.textSecondary, fontSize = 12.sp)
            if (detailError) Text(stringResource(R.string.detail_unavailable), color = Zw.warningText, fontSize = 13.sp)

            // "Apri in Zabbix": only with a frontend URL configured by the admin; Custom Tabs, never a WebView
            val frontend = detail?.frontendUrl
            if (frontend != null && (frontend.startsWith("https://") || frontend.startsWith("http://"))) {
                OutlinedButton(onClick = { openInBrowser(ctx, frontend) }) { Text(stringResource(R.string.detail_open_zabbix)) }
            }
            // Silence: no new notifications for this alarm (repeats of Zabbix, reminders); updates and the
            // recovery still arrive, without sound. It can be turned off again.
            if (r.sid != null && status != "resolved") {
                val silenced = local.any { it.silenced }
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    OutlinedButton(onClick = {
                        vm.setSilenced(r.serverRef, r.sid, !silenced)
                        local = local.map { it.copy(silenced = !silenced) }
                    }) { Text(stringResource(if (silenced) R.string.action_unsilence else R.string.action_silence)) }
                    if (silenced) Text(stringResource(R.string.detail_silenced), color = Zw.textSecondary, fontSize = 13.sp)
                }
            }

            SectionTitle(inset = false, text = stringResource(R.string.detail_history))
            val history = detail?.history
            // Repeats of Zabbix (another escalation step) are known only to the phone: merged by time
            val repeats = local.filter { it.kind == "repeat" }.map { it.receivedAt }
            if (history != null) {
                if (history.isEmpty() && repeats.isEmpty()) Text(stringResource(R.string.detail_no_history), color = Zw.textSecondary, fontSize = 13.sp)
                (history.map { isoMs(it.clock) to it } + repeats.map { it to null }).sortedBy { it.first }.forEach { (at, h) ->
                    if (h != null) HistoryRow(h) else RepeatRow(at)
                }
            } else {
                local.forEach { m ->
                    Text("${timeShort(m.receivedAt)}  ${kindText(m.kind)}", color = Zw.textBody, fontSize = 13.sp)
                }
            }

            if (source?.apiMode == "read_ack" && status != "resolved") {
                SectionTitle(inset = false, text = stringResource(R.string.detail_ack))
                AckBox(onSend = { vm.sendAck(r.serverRef, r.source, r.eventId, it) })
            }
            acks.forEach { AckStateRow(it) }
        }
    }
}

@Composable
private fun Field(label: Int, value: String, color: Color? = null) {
    if (value.isEmpty()) return
    Row {
        Text(stringResource(label), color = Zw.textSecondary, fontSize = 13.sp, modifier = Modifier.width(96.dp))
        Text(value, color = color ?: Zw.textBody, fontSize = 14.sp, fontWeight = if (color != null) FontWeight.Bold else null)
    }
}

@Composable
private fun HistoryRow(h: HistoryEntry) {
    val who = when {
        h.authorKind == "app_user" -> stringResource(R.string.history_via_zweep, h.authorName ?: "")
        !h.authorName.isNullOrEmpty() -> h.authorName
        else -> stringResource(R.string.history_zabbix_user)
    }
    val what = buildList {
        if ("ack" in h.actions) add(stringResource(R.string.history_ack))
        if ("unack" in h.actions) add(stringResource(R.string.history_unack))
        if ("close" in h.actions) add(stringResource(R.string.history_close))
        if ("severity" in h.actions && h.oldSeverity != null && h.newSeverity != null) {
            add(stringResource(R.string.history_severity, stringResource(Notifier.severityLabel(h.oldSeverity)), stringResource(Notifier.severityLabel(h.newSeverity))))
        }
        if ("suppress" in h.actions) add(stringResource(R.string.history_suppress))
    }.joinToString(" · ")
    Column(Modifier.fillMaxWidth().background(Zw.surface).padding(10.dp)) {
        Text("${timeShort(isoMs(h.clock))}  $who", color = Zw.textPrimary, fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
        if (what.isNotEmpty()) Text(what, color = Zw.textBody, fontSize = 13.sp)
        if (!h.message.isNullOrEmpty()) Text("“${h.message}”", color = Zw.textBody, fontSize = 13.sp)
    }
}

/** A new notification of Zabbix for the same problem (another escalation step) */
@Composable
private fun RepeatRow(at: Long) {
    Column(Modifier.fillMaxWidth().background(Zw.surface).padding(10.dp)) {
        Text("${timeShort(at)}  Zabbix", color = Zw.textPrimary, fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
        Text(stringResource(R.string.history_repeat), color = Zw.textBody, fontSize = 13.sp)
    }
}

@Composable
private fun AckBox(onSend: (String) -> Unit) {
    var text by remember { mutableStateOf("") }
    val valid = normalizeAckText(text) != null
    OutlinedTextField(text, { if (it.length <= 1000) text = it }, Modifier.fillMaxWidth(), label = { Text(stringResource(R.string.ack_message)) }, minLines = 2)
    Button(enabled = valid, onClick = {
        normalizeAckText(text)?.let(onSend)
        text = ""
    }) { Text(stringResource(R.string.ack_send)) }
}

@Composable
private fun AckStateRow(a: AckEntity) {
    val (label, color) = when (a.state) {
        AckEntity.QUEUED -> stringResource(R.string.ack_queued) to Zw.warningText
        AckEntity.ACCEPTED -> stringResource(R.string.ack_accepted) to Zw.textBody
        AckEntity.CONFIRMED -> stringResource(R.string.ack_confirmed) to Zw.success
        else -> stringResource(R.string.ack_rejected, a.reason ?: "-") to Zw.errorText
    }
    Column(Modifier.padding(top = 4.dp)) {
        Text(label, color = color, fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
        Text("“${a.text}”", color = Zw.textSecondary, fontSize = 12.sp)
    }
}

@Composable
private fun statusText(status: String) = stringResource(
    when (status) {
        "resolved", "gone" -> R.string.state_resolved
        "acknowledged" -> R.string.state_acked
        "suppressed" -> R.string.state_suppressed
        else -> R.string.state_open
    },
)

@Composable
private fun kindText(kind: String) = stringResource(
    when (kind) {
        "recovery" -> R.string.state_resolved
        "update" -> R.string.state_updated
        "test" -> R.string.state_test
        "repeat" -> R.string.history_repeat
        else -> R.string.state_open
    },
)

/** Custom Tabs (system browser: login, password manager and 2FA stay there), fallback to any browser */
fun openInBrowser(ctx: Context, url: String) {
    val uri = Uri.parse(url)
    runCatching { CustomTabsIntent.Builder().setShowTitle(true).build().launchUrl(ctx, uri) }
        .onFailure { runCatching { ctx.startActivity(Intent(Intent.ACTION_VIEW, uri).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) } }
}

/** Who closed a forcibly closed alert and why ("manual" or "orphan"), from the message body */
private fun closedInfo(body: String): Pair<String, String>? = runCatching {
    val c = net.nicodroid.zweep.core.WireJson.parseToJsonElement(body).jsonObject["closed"]?.jsonObject ?: return@runCatching null
    (c["by"]?.jsonPrimitive?.content ?: "") to (c["reason"]?.jsonPrimitive?.content ?: "manual")
}.getOrNull()
