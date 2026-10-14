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
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
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
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
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

/** A question asked before an action of the detail: nothing is done without a yes */
private class Confirm(val title: String, val text: String, val yes: String, val run: () -> Unit)

/** Data of Zabbix older than this are said to be old, in the detail and before an update */
private const val STALE_AFTER_MS = 120_000L

/** The clocks of the phone and of Zabbix may differ: a message of the History is the one just sent within this */
private const val HISTORY_CLOCK_SLACK_MS = 300_000L

/** How often an open detail reads the data of Zabbix again */
private const val DETAIL_REFRESH_MS = 30_000L

@Composable
fun DetailScreen(vm: AppViewModel, r: Route.Detail, onBack: () -> Unit) {
    val ctx = LocalContext.current
    var confirm by remember { mutableStateOf<Confirm?>(null) }
    var closeStep by remember { mutableStateOf(0) }
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
    // While the detail stays open the data of Zabbix are read again, as the server does: what is on
    // screen is current, and "old data" means that Zabbix or the server really stopped answering
    LaunchedEffect(r) {
        while (true) {
            kotlinx.coroutines.delay(DETAIL_REFRESH_MS)
            if (source?.apiMode != null && source?.apiMode != "disabled") {
                vm.detail(r.serverRef, r.source, r.eventId).onSuccess { detail = it; detailError = false }
            }
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
            // Old data are said to be old: what a colleague did in the meantime may not show
            val asOf = isoMs(detail?.dataAsOf)
            val stale = if (asOf > 0 && System.currentTimeMillis() - asOf > STALE_AFTER_MS) stringResource(R.string.detail_stale, timeShort(asOf)) else null
            if (stale != null) Text(stale, color = Zw.warningText, fontSize = 13.sp)
            else if (asOf > 0) Text(stringResource(R.string.detail_data_as_of, timeShort(asOf)), color = Zw.textSecondary, fontSize = 12.sp)
            if (detailError) Text(stringResource(R.string.detail_unavailable), color = Zw.warningText, fontSize = 13.sp)

            // Actions: each asks before it acts
            // "Open in Zabbix": only with a frontend URL configured by the admin; Custom Tabs, never a WebView
            val frontend = detail?.frontendUrl?.takeIf { it.startsWith("https://") || it.startsWith("http://") }
            val test = first?.kind == "test"
            // Silence: no new notifications for this alarm (repeats of Zabbix, reminders); updates and the
            // recovery still arrive, without sound. It can be turned off again.
            val silenced = local.any { it.silenced }
            val canSilence = r.sid != null && !test && (status != "resolved" || silenced)
            // Forced close: for who the administrator allowed, on an alert still open
            val canClose = r.sid != null && !test && status != "resolved" && vm.canClose(r.serverRef)
            if (frontend != null || canSilence || canClose) {
                SectionTitle(inset = false, text = stringResource(R.string.detail_actions))
                // Open in Zabbix on a line of its own, Silence and Force close below it
                if (frontend != null) {
                    val title = stringResource(R.string.open_zabbix_title)
                    val text = stringResource(R.string.open_zabbix_text)
                    val yes = stringResource(R.string.action_open)
                    OutlinedButton(shape = Zw.control, onClick = { confirm = Confirm(title, text, yes) { openInBrowser(ctx, frontend) } }) { Text(stringResource(R.string.detail_open_zabbix)) }
                }
                if (canSilence || canClose) {
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        if (canSilence) {
                            val label = stringResource(if (silenced) R.string.action_unsilence else R.string.action_silence)
                            val text = stringResource(if (silenced) R.string.unsilence_confirm else R.string.silence_confirm)
                            OutlinedButton(shape = Zw.control, onClick = {
                                confirm = Confirm(label, text, label) {
                                    vm.setSilenced(r.serverRef, r.sid!!, !silenced)
                                    local = local.map { it.copy(silenced = !silenced) }
                                }
                            }) { Text(label) }
                        }
                        if (canClose) {
                            OutlinedButton(shape = Zw.control, onClick = { closeStep = 1 }) { Text(stringResource(R.string.close_alert_action), color = Zw.errorText) }
                        }
                    }
                }
                if (silenced) Text(stringResource(R.string.detail_silenced), color = Zw.textSecondary, fontSize = 13.sp)
            }
            confirm?.let { c ->
                AlertDialog(
                    onDismissRequest = { confirm = null },
                    title = { Text(c.title) },
                    text = { Text(c.text) },
                    confirmButton = { TextButton({ confirm = null; c.run() }) { Text(c.yes) } },
                    dismissButton = { TextButton({ confirm = null }) { Text(stringResource(R.string.action_cancel)) } },
                )
            }
            if (closeStep > 0 && r.sid != null) {
                CloseAlertDialogs(vm, r.serverRef, r.sid, zabbixOpen = p?.isOpen == true, step = closeStep, setStep = { closeStep = it }, dismiss = { closeStep = 0 }, onClosed = onBack)
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

            // "Update problem", with the words of Zabbix: a message alone (Update), or an acknowledgement
            // with it. Nothing is done blind: without the data nobody knows what colleagues did meanwhile.
            if (source?.apiMode == "read_ack" && status != "resolved" && !test && (p != null || detailError)) {
                SectionTitle(inset = false, text = stringResource(R.string.detail_update))
                if (p == null) {
                    Text(stringResource(R.string.update_need_data), color = Zw.textSecondary, fontSize = 13.sp)
                } else {
                    // Zabbix keeps one acknowledgement per problem: on one already acknowledged an
                    // Acknowledge adds its message, and is sent as what it is when the server can
                    val canUpdate = vm.canUpdate(r.serverRef)
                    UpdateBox(acked = p.acknowledged, canUpdate = canUpdate, stale = stale) { text, action ->
                        vm.sendAck(r.serverRef, r.source, r.eventId, text, if (p.acknowledged && canUpdate) AckEntity.ACTION_MESSAGE else action)
                    }
                }
            }
            // The answer of Zabbix stays under the box until the History shows the message itself: said
            // twice it would only be in the way
            acks.filterNot { a ->
                a.state == AckEntity.CONFIRMED && detail?.history?.any { h ->
                    h.authorKind == "app_user" && h.message == a.text && isoMs(h.clock) >= a.createdAt - HISTORY_CLOCK_SLACK_MS
                } == true
            }.forEach { AckStateRow(it) }
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
        if ("unack" in h.actions) add(stringResource(R.string.history_unack))
        if ("close" in h.actions) add(stringResource(R.string.history_close))
        if ("severity" in h.actions && h.oldSeverity != null && h.newSeverity != null) {
            add(stringResource(R.string.history_severity, stringResource(Notifier.severityLabel(h.oldSeverity)), stringResource(Notifier.severityLabel(h.newSeverity))))
        }
        if ("suppress" in h.actions) add(stringResource(R.string.history_suppress))
    }.joinToString(" · ")
    Column(Modifier.fillMaxWidth().background(Zw.surface).padding(10.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("${timeShort(isoMs(h.clock))}  $who", color = Zw.textPrimary, fontSize = 13.sp, fontWeight = FontWeight.SemiBold, modifier = Modifier.weight(1f))
            // The update that carried the acknowledgement has the label of the Alerts on the right; the others nothing
            if ("ack" in h.actions) Text(stringResource(R.string.state_acked).uppercase(), color = Zw.textBody, fontSize = 10.sp, fontWeight = FontWeight.Bold, maxLines = 1)
        }
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

/**
 * The message and the two buttons of "Update problem": filled, to tell them from the actions above,
 * Update blue and Acknowledge in the red of the brand. Update only with a server that can add a
 * message without acknowledging. Each asks before sending, and says so when the data are old.
 */
@Composable
private fun UpdateBox(acked: Boolean, canUpdate: Boolean, stale: String?, onSend: (String, String) -> Unit) {
    var text by remember { mutableStateOf("") }
    var pending by remember { mutableStateOf<String?>(null) } // the action waiting for a yes
    // The keyboard goes away with the question: it must not come back over the answer of Zabbix
    val focus = LocalFocusManager.current
    val message = normalizeAckText(text)
    OutlinedTextField(text, { if (it.length <= 1000) text = it }, Modifier.fillMaxWidth(), label = { Text(stringResource(R.string.ack_message)) }, minLines = 2)
    Row(Modifier.padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        if (canUpdate) {
            Button(enabled = message != null, shape = Zw.control, onClick = { focus.clearFocus(); pending = AckEntity.ACTION_MESSAGE },
                colors = ButtonDefaults.buttonColors(containerColor = Zw.accent, contentColor = Color.White)) { Text(stringResource(R.string.action_update)) }
        }
        Button(enabled = message != null, shape = Zw.control, onClick = { focus.clearFocus(); pending = AckEntity.ACTION_ACK },
            colors = ButtonDefaults.buttonColors(containerColor = Zw.brand, contentColor = Color.White)) { Text(stringResource(R.string.ack_send)) }
    }
    val action = pending
    if (action != null && message != null) {
        val question = stringResource(
            when {
                action == AckEntity.ACTION_ACK && acked -> R.string.ack_confirm_already
                action == AckEntity.ACTION_ACK -> R.string.ack_confirm
                acked -> R.string.update_confirm_acked
                else -> R.string.update_confirm
            },
        )
        AlertDialog(
            onDismissRequest = { pending = null },
            title = { Text(stringResource(if (action == AckEntity.ACTION_ACK) R.string.ack_send else R.string.action_update)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                    Text(question)
                    Text("“$message”", fontWeight = FontWeight.SemiBold, maxLines = 6, overflow = TextOverflow.Ellipsis)
                    if (stale != null) Text(stale, color = Zw.warningText)
                }
            },
            confirmButton = { TextButton({ pending = null; onSend(message, action); text = "" }) { Text(stringResource(R.string.action_yes)) } },
            dismissButton = { TextButton({ pending = null }) { Text(stringResource(R.string.action_no)) } },
        )
    }
}

/**
 * Forced close of an alert, from the Actions of its detail: 1) what it does (and whether Zabbix
 * still has the problem open), 2) the operation is irreversible. The recovery reaches every recipient.
 */
@Composable
fun CloseAlertDialogs(vm: AppViewModel, serverRef: Long, sid: String, zabbixOpen: Boolean, step: Int, setStep: (Int) -> Unit, dismiss: () -> Unit, onClosed: () -> Unit) {
    val ctx = LocalContext.current
    var busy by remember { mutableStateOf(false) }
    when (step) {
        1 -> AlertDialog(
            onDismissRequest = dismiss,
            title = { Text(stringResource(R.string.close_alert_title)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                    Text(stringResource(R.string.close_alert_warning1))
                    if (zabbixOpen) Text(stringResource(R.string.close_alert_zabbix_open), color = Zw.warningText, fontWeight = FontWeight.SemiBold)
                }
            },
            confirmButton = { TextButton({ setStep(2) }) { Text(stringResource(R.string.action_continue)) } },
            dismissButton = { TextButton(dismiss) { Text(stringResource(R.string.action_cancel)) } },
        )
        2 -> AlertDialog(
            onDismissRequest = { if (!busy) dismiss() },
            title = { Text(stringResource(R.string.close_alert_title)) },
            text = { Text(stringResource(R.string.close_alert_warning2), color = Zw.errorText, fontWeight = FontWeight.SemiBold) },
            confirmButton = {
                TextButton(enabled = !busy, onClick = {
                    busy = true
                    vm.closeAlert(serverRef, sid) { err ->
                        busy = false
                        val msg = when (err) {
                            null -> R.string.close_alert_done
                            "close_not_allowed" -> R.string.close_alert_not_allowed
                            "alert_not_open" -> R.string.close_alert_not_open
                            else -> R.string.close_alert_failed
                        }
                        android.widget.Toast.makeText(ctx, ctx.getString(msg), android.widget.Toast.LENGTH_LONG).show()
                        dismiss()
                        if (err == null) onClosed()
                    }
                }) { Text(stringResource(R.string.close_alert_confirm), color = Zw.errorText) }
            },
            dismissButton = { TextButton(enabled = !busy, onClick = dismiss) { Text(stringResource(R.string.action_cancel)) } },
        )
    }
}

@Composable
private fun AckStateRow(a: AckEntity) {
    val (label, color) = when (a.state) {
        AckEntity.QUEUED -> stringResource(R.string.ack_queued) to Zw.warningText
        AckEntity.ACCEPTED -> stringResource(R.string.ack_accepted) to Zw.textBody
        AckEntity.CONFIRMED -> stringResource(if (a.action == AckEntity.ACTION_MESSAGE) R.string.ack_confirmed_message else R.string.ack_confirmed) to Zw.success
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
