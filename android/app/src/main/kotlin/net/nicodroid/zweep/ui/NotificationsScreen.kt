// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.AssistChip
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.delay
import net.nicodroid.zweep.R
import net.nicodroid.zweep.core.NotShownReason
import net.nicodroid.zweep.data.Settings
import net.nicodroid.zweep.service.ConnState
import net.nicodroid.zweep.service.Notifier
import net.nicodroid.zweep.service.Permissions

@Composable
fun NotificationsScreen(vm: AppViewModel, open: (EventCard) -> Unit) {
    val events by vm.events.collectAsStateWithLifecycle()
    val servers by vm.servers.collectAsStateWithLifecycle()
    // Durations of open problems move on every minute
    val now by produceState(System.currentTimeMillis()) { while (true) { delay(60_000); value = System.currentTimeMillis() } }
    var filters by remember { mutableStateOf(Filters()) }
    val settings by vm.settings.collectAsStateWithLifecycle()
    val sevShown = vm.severities(settings, "filter_sev_events")
    val status = settings["filter_status_events"] ?: "all"
    // active (default), history, or all
    val view = settings[Settings.NOTIF_VIEW]?.takeIf { it == "history" || it == "all" } ?: "active"
    var search by rememberSaveable { mutableStateOf(false) }
    var confirmRead by remember { mutableStateOf(false) }
    // Forced close: long press on an active alert, then two warnings
    var closing by remember { mutableStateOf<EventCard?>(null) }
    var closeStep by remember { mutableStateOf(0) }
    val ctx = LocalContext.current
    val active = events.count { !it.archived }
    val shown = events.filter { e ->
        (view == "all" || e.archived == (view == "history")) && e.sev.coerceAtLeast(0) in sevShown && when (status) {
            "open" -> e.kind != "recovery"
            "resolved" -> e.kind == "recovery"
            "acked" -> e.acknowledged && e.kind != "recovery"
            else -> true
        } &&
            (filters.query.isBlank() || listOf(e.title, e.host, e.sourceName).any { it.contains(filters.query, true) })
    }
    Column(Modifier.fillMaxSize()) {
        Header(stringResource(R.string.app_name), titleSize = 24) {
            IconButton(onClick = { confirmRead = true }) { Icon(painterResource(R.drawable.ic_done_all), stringResource(R.string.action_mark_all_read), tint = Zw.textBody) }
            IconButton(onClick = { search = !search }) { Icon(painterResource(R.drawable.ic_search), stringResource(R.string.action_search), tint = Zw.textBody) }
        }
        StatusStrip(vm)
        // Same placement and size as the search field of the Problems tab
        if (search) {
            OutlinedTextField(filters.query, { filters = filters.copy(query = it) }, Modifier.fillMaxWidth().padding(start = 12.dp, end = 12.dp, top = 12.dp, bottom = 4.dp),
                placeholder = { Text(stringResource(R.string.search_hint)) }, singleLine = true)
        }
        Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 12.dp, vertical = 6.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            // Active: what still needs attention; History: resolved for a while, tests already seen; All: both
            ChoiceFilter(
                listOf(
                    "active" to stringResource(R.string.view_active, active), "history" to stringResource(R.string.view_history, events.size - active),
                    "all" to stringResource(R.string.view_all, events.size),
                ),
                view,
            ) { vm.setSetting(Settings.NOTIF_VIEW, it) }
            SeverityFilter(sevShown) { vm.setSeverities("filter_sev_events", it) }
            ChoiceFilter(
                listOf(
                    "all" to stringResource(R.string.filter_status_all), "open" to stringResource(R.string.filter_open_only),
                    "resolved" to stringResource(R.string.state_resolved), "acked" to stringResource(R.string.state_acked),
                ),
                status,
                ) { vm.setSetting("filter_status_events", it) }
        }
        if (confirmRead) {
            AlertDialog(
                onDismissRequest = { confirmRead = false },
                text = { Text(stringResource(R.string.mark_all_read_confirm)) },
                confirmButton = { TextButton({ confirmRead = false; vm.markAllRead() }) { Text(stringResource(R.string.action_mark_all_read)) } },
                dismissButton = { TextButton({ confirmRead = false }) { Text(stringResource(R.string.action_cancel)) } },
            )
        }
        closing?.let { c -> CloseAlertDialogs(vm, c, closeStep, { closeStep = it }) { closing = null; closeStep = 0 } }
        if (Permissions.essentialMissing(ctx)) {
            Text(stringResource(R.string.banner_permissions), color = Zw.warningText, fontSize = 13.sp,
                modifier = Modifier.fillMaxWidth().background(Zw.surface).padding(12.dp))
        }
        if (shown.isEmpty()) {
            Column(Modifier.fillMaxSize().padding(32.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
                Text(stringResource(if (events.isEmpty()) R.string.empty_no_notifications else R.string.empty_no_match), color = Zw.textPrimary, fontWeight = FontWeight.Bold)
                Text(stringResource(if (events.isEmpty()) R.string.empty_no_notifications_hint else R.string.empty_no_match_hint), color = Zw.textSecondary, fontSize = 13.sp)
            }
        } else {
            LazyColumn(Modifier.fillMaxSize()) {
                items(shown, key = { "${it.serverRef}/${it.sid}" }) { e ->
                    val closable = e.kind != "recovery" && e.kind != "test" && vm.canClose(e.serverRef)
                    EventRow(e, now, servers.size > 1, onLongClick = if (closable) ({ closing = e; closeStep = 1 }) else null) { open(e) }
                }
            }
        }
    }
}

@Composable
private fun EventRow(e: EventCard, now: Long, showServer: Boolean, onLongClick: (() -> Unit)? = null, onClick: () -> Unit) {
    val open = e.kind != "recovery" && e.kind != "test"
    Row(Modifier.fillMaxWidth().height(IntrinsicSize.Min).combinedClickable(onClick = onClick, onLongClick = onLongClick).padding(horizontal = 12.dp, vertical = 6.dp)) {
        val custom = e.channelName.isNotEmpty()
        SeverityBar(e.sev, color = if (custom) channelColor(e.channelColor) else null)
        Column(Modifier.weight(1f).padding(start = 10.dp)) {
            // Right column (time, duration, status) shares one right edge; the host always starts at the same place
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (custom) ChannelBadge(e.channelName, e.channelColor) else SeverityBadge(e.sev)
                Text(e.host, color = Zw.textBody, fontSize = 12.sp, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                if (e.unread) UnreadDot()
                Text(timeShort(e.firstAt), color = Zw.textSecondary, fontSize = 12.sp)
            }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.padding(top = 2.dp)) {
                Text(e.name, color = Zw.textPrimary, fontSize = 15.sp, fontWeight = if (e.unread) FontWeight.SemiBold else FontWeight.Normal,
                    maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                if (open) Text(duration(e.firstAt, now), color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(top = 2.dp))
            }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(top = 2.dp)) {
                // The server is only worth showing when there is more than one
                val origin = if (showServer) listOf(e.sourceName, e.serverLabel).filter { it.isNotEmpty() }.distinct().joinToString(" · ") else e.sourceName
                Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
                    Text(origin, color = Zw.textSecondary, fontSize = 12.sp, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                }
                when {
                    e.kind == "test" -> StatusLabel(stringResource(R.string.state_test), Zw.accent)
                    e.kind == "recovery" -> StatusLabel(stringResource(R.string.state_resolved), Zw.success)
                    e.acknowledged -> StatusLabel(stringResource(R.string.state_acked), Zw.textBody)
                    else -> StatusLabel(stringResource(R.string.state_open), Zw.errorText)
                }
            }
            if (e.silentReason != null) {
                Text(stringResource(R.string.silent_reason, reasonText(e.silentReason)), color = Zw.textSecondary, fontSize = 11.sp)
            }
        }
    }
    Spacer(Modifier.fillMaxWidth().height(1.dp).background(Zw.outline))
}

@Composable
fun TextAction(text: String, onClick: () -> Unit) = TextButton(onClick = onClick) { Text(text) }

@Composable
private fun reasonText(reason: String): String = when (reason) {
    NotShownReason.APP_DND -> stringResource(R.string.reason_app_dnd)
    NotShownReason.OLD -> stringResource(R.string.reason_old)
    NotShownReason.CHANNEL_OFF -> stringResource(R.string.reason_channel_off)
    NotShownReason.CAP -> stringResource(R.string.reason_cap)
    NotShownReason.PERMISSION -> stringResource(R.string.reason_permission)
    NotShownReason.FILTER -> stringResource(R.string.reason_filter)
    else -> reason
}

/**
 * Forced close of an alert: 1) the option, 2) what it does (and whether Zabbix still has the
 * problem open), 3) the operation is irreversible. The recovery reaches every recipient.
 */
@Composable
private fun CloseAlertDialogs(vm: AppViewModel, e: EventCard, step: Int, setStep: (Int) -> Unit, dismiss: () -> Unit) {
    val ctx = LocalContext.current
    var busy by remember { mutableStateOf(false) }
    when (step) {
        1 -> AlertDialog(
            onDismissRequest = dismiss,
            title = { Text(e.name, maxLines = 2, overflow = TextOverflow.Ellipsis) },
            text = { Text(e.host, color = Zw.textSecondary) },
            confirmButton = { TextButton({ setStep(2) }) { Text(stringResource(R.string.close_alert_action), color = Zw.errorText) } },
            dismissButton = { TextButton(dismiss) { Text(stringResource(R.string.action_cancel)) } },
        )
        2 -> AlertDialog(
            onDismissRequest = dismiss,
            title = { Text(stringResource(R.string.close_alert_title)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                    Text(stringResource(R.string.close_alert_warning1))
                    if (vm.zabbixOpen(e)) Text(stringResource(R.string.close_alert_zabbix_open), color = Zw.warningText, fontWeight = FontWeight.SemiBold)
                }
            },
            confirmButton = { TextButton({ setStep(3) }) { Text(stringResource(R.string.action_continue)) } },
            dismissButton = { TextButton(dismiss) { Text(stringResource(R.string.action_cancel)) } },
        )
        3 -> AlertDialog(
            onDismissRequest = { if (!busy) dismiss() },
            title = { Text(stringResource(R.string.close_alert_title)) },
            text = { Text(stringResource(R.string.close_alert_warning2), color = Zw.errorText, fontWeight = FontWeight.SemiBold) },
            confirmButton = {
                TextButton(enabled = !busy, onClick = {
                    busy = true
                    vm.closeAlert(e.serverRef, e.sid) { err ->
                        busy = false
                        val msg = when (err) {
                            null -> R.string.close_alert_done
                            "close_not_allowed" -> R.string.close_alert_not_allowed
                            "alert_not_open" -> R.string.close_alert_not_open
                            else -> R.string.close_alert_failed
                        }
                        android.widget.Toast.makeText(ctx, ctx.getString(msg), android.widget.Toast.LENGTH_LONG).show()
                        dismiss()
                    }
                }) { Text(stringResource(R.string.close_alert_confirm), color = Zw.errorText) }
            },
            dismissButton = { TextButton(enabled = !busy, onClick = dismiss) { Text(stringResource(R.string.action_cancel)) } },
        )
    }
}
