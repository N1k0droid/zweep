// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.produceState
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.delay
import net.nicodroid.zweep.R
import net.nicodroid.zweep.data.Settings
import net.nicodroid.zweep.service.ConnState
import net.nicodroid.zweep.service.Notifier

/** Views of the Problems tab, as in the Zabbix problem list */
private const val VIEW_RECENT = "recent"
private const val VIEW_PROBLEMS = "problems"
private const val VIEW_HISTORY = "history"

@Composable
fun ProblemsScreen(vm: AppViewModel, open: (ProblemItem) -> Unit) {
    val live by vm.problems.collectAsStateWithLifecycle()
    val servers by vm.servers.collectAsStateWithLifecycle()
    val views by vm.hasProblemViews.collectAsStateWithLifecycle()
    val now by produceState(System.currentTimeMillis()) { while (true) { delay(60_000); value = System.currentTimeMillis() } }
    val settings by vm.settings.collectAsStateWithLifecycle()
    // The three views of the Zabbix problem list filter the same list, kept in sync in the background
    // (open problems plus those resolved in the last 7 days). Recent: open plus resolved for
    // "Resolved stay in Active for"; Problems: open only; History: started in the chosen period.
    val view = if (views) settings["filter_view_problems"] ?: VIEW_RECENT else VIEW_PROBLEMS
    val resolvedMs = (settings[Settings.RESOLVED_MINUTES]?.toLongOrNull() ?: Settings.DEFAULT_RESOLVED_MINUTES.toLong()) * 60_000L
    val period = Settings.problemHistoryMinutes(settings[Settings.PROBLEM_HISTORY_MINUTES])
    // One line per server that cannot be reached now, with the time its data were last current
    val links by vm.links.collectAsStateWithLifecycle()
    val liveUntil by vm.liveUntil.collectAsStateWithLifecycle()
    val offline = servers.filter { it.enabled && "problems" in it.features.split(',') && links[it.id]?.state != ConnState.CONNECTED }
        .map { HistoryNotice(it.label, liveUntil[it.id]) }
    // Sources shown: all by default; the user unticks the ones to hide (new sources appear at once)
    val sources = live.map { sourceKey(it) to sourceLabel(it, servers.size > 1) }.distinct().sortedBy { it.second.lowercase() }
    val srcHidden = settings["filter_src_hidden_problems"]?.split('\n')?.filter { it.isNotEmpty() }?.toSet() ?: emptySet()
    val shownLive = live.filter { sourceKey(it) !in srcHidden }
    val all = when (view) {
        VIEW_HISTORY -> shownLive.filter { now - isoMs(it.row.clock) <= period * 60_000L }
        VIEW_RECENT -> shownLive.filter { it.row.isOpen || (it.row.isResolved && now - isoMs(it.row.rClock) <= resolvedMs) }
        else -> shownLive.filter { it.row.isOpen }
    }
    val sevShown = vm.severities(settings, "filter_sev_problems")
    val status = settings["filter_status_problems"] ?: "all"
    val hgShown = settings["filter_hg_problems"]?.split('\n')?.filter { it.isNotEmpty() }?.toSet() ?: emptySet()
    val hostGroups = all.flatMap { it.row.hostgroups }.distinct()
    var query by remember { mutableStateOf("") }
    var search by rememberSaveable { mutableStateOf(false) }
    val list = all.filter { p ->
        p.row.severity in sevShown && inHostGroups(p.row.hostgroups, hgShown) && when (status) {
            "unacked" -> p.row.isOpen && !p.row.acknowledged
            "acked" -> p.row.isOpen && p.row.acknowledged
            "resolved" -> !p.row.isOpen
            else -> true
        } &&
            (query.isBlank() || listOf(p.row.name, p.row.hosts.joinToString { it.name }, p.row.hostgroups.joinToString()).any { it.contains(query, true) })
    }.sortedByDescending { isoMs(it.row.clock) }
    val stale = live.filter { it.stale }.map { it.sourceName }.distinct()
    Column(Modifier.fillMaxSize()) {
        Header(stringResource(R.string.problems_title, list.size)) {
            if (sources.size > 1 || srcHidden.isNotEmpty()) {
                SourceFilter(sources, srcHidden) { vm.setSetting("filter_src_hidden_problems", it.sorted().joinToString("\n")) }
            }
            IconButton(onClick = { vm.refreshProblems() }) {
                Icon(painterResource(R.drawable.ic_refresh), stringResource(R.string.action_refresh), tint = Zw.textBody)
            }
            IconButton(onClick = { search = !search }) { Icon(painterResource(R.drawable.ic_search), stringResource(R.string.action_search), tint = Zw.textBody) }
        }
        StatusStrip(vm)
        // The search field appears with the search icon, as in the Alerts tab
        if (search) {
            OutlinedTextField(query, { query = it }, Modifier.fillMaxWidth().padding(start = 12.dp, end = 12.dp, top = 12.dp, bottom = 4.dp),
                placeholder = { Text(stringResource(R.string.search_problems_hint)) }, singleLine = true)
        }
        Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 12.dp, vertical = 6.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            SeverityFilter(sevShown) { vm.setSeverities("filter_sev_problems", it) }
            HostGroupFilter(hostGroups, hgShown) { vm.setSetting("filter_hg_problems", it.sorted().joinToString("\n")) }
            if (views) {
                // View of the Zabbix problem list; History shows its period (Settings → Problem history)
                ChoiceFilter(
                    listOf(
                        VIEW_RECENT to stringResource(R.string.problems_view_recent),
                        VIEW_PROBLEMS to stringResource(R.string.problems_view_problems),
                        VIEW_HISTORY to stringResource(R.string.problems_view_history) + " · " + periodLabel(period),
                    ),
                    view,
                ) { vm.setSetting("filter_view_problems", it) }
            }
            ChoiceFilter(
                listOf("all" to stringResource(R.string.filter_status_all), "unacked" to stringResource(R.string.filter_unacked),
                    "acked" to stringResource(R.string.state_acked), "resolved" to stringResource(R.string.state_resolved)),
                status,
            ) { vm.setSetting("filter_status_problems", it) }
        }
        // Warnings under the filters, the same in every view
        if (offline.isNotEmpty()) Notices(offline)
        if (stale.isNotEmpty()) {
            Text(stringResource(R.string.problems_stale, stale.joinToString()), color = Zw.errorText, fontSize = 13.sp,
                modifier = Modifier.fillMaxWidth().background(Zw.surface).padding(12.dp))
        }
        if (list.isEmpty()) {
            Text(stringResource(R.string.problems_all_clear), color = Zw.textSecondary, modifier = Modifier.padding(24.dp))
        } else {
            LazyColumn(Modifier.fillMaxSize()) {
                items(list, key = { "${it.serverRef}/${it.row.source}/${it.row.eventid}" }) { p -> ProblemRowView(p, now, servers.size > 1) { open(p) } }
            }
        }
    }
}

private fun sourceKey(p: ProblemItem) = "${p.serverRef}/${p.row.source}"

private fun sourceLabel(p: ProblemItem, showServer: Boolean) =
    if (showServer) listOf(p.sourceName, p.serverLabel).filter { it.isNotEmpty() }.distinct().joinToString(" · ") else p.sourceName

/** Top bar filter of the Zabbix sources: a tick per source; the icon is highlighted while some are hidden */
@Composable
private fun SourceFilter(sources: List<Pair<String, String>>, hidden: Set<String>, onChange: (Set<String>) -> Unit) {
    var open by remember { mutableStateOf(false) }
    Box {
        IconButton(onClick = { open = true }) {
            Icon(painterResource(R.drawable.ic_filter_list), stringResource(R.string.filter_sources), tint = if (hidden.isEmpty()) Zw.textBody else Zw.accent)
        }
        DropdownMenu(open, { open = false }) {
            Text(stringResource(R.string.filter_sources), color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp))
            sources.forEach { (key, label) ->
                val on = key !in hidden
                DropdownMenuItem(
                    text = { Text(label, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                    leadingIcon = { Checkbox(on, null) },
                    // At least one source stays shown
                    onClick = { if (!on) onChange(hidden - key) else if (sources.count { it.first !in hidden } > 1) onChange(hidden + key) },
                )
            }
            if (hidden.isNotEmpty()) {
                DropdownMenuItem(
                    text = { Text(stringResource(R.string.filter_select_all), modifier = Modifier.fillMaxWidth(), textAlign = TextAlign.Center) },
                    onClick = { onChange(emptySet()) },
                )
            }
        }
    }
}

/** One line per server or source that cannot be reached, with the time of the data shown (names can be long) */
@Composable
private fun Notices(list: List<HistoryNotice>) {
    Column(Modifier.fillMaxWidth().background(Zw.surface).padding(horizontal = 12.dp, vertical = 8.dp)) {
        list.forEach { n ->
            Text(
                "⚠ " + if (n.asOf != null) stringResource(R.string.history_not_reachable, n.name, timeShort(n.asOf))
                else stringResource(R.string.history_not_reachable_no_data, n.name),
                color = Zw.errorText, fontSize = 13.sp,
            )
        }
    }
}

@Composable
private fun ProblemRowView(p: ProblemItem, now: Long, showServer: Boolean, onClick: () -> Unit) {
    val since = isoMs(p.row.clock)
    Row(Modifier.fillMaxWidth().height(IntrinsicSize.Min).clickable(onClick = onClick).padding(horizontal = 12.dp, vertical = 6.dp)) {
        SeverityBar(p.row.severity)
        Column(Modifier.weight(1f).padding(start = 10.dp)) {
            // Same layout as the notification rows: start time, duration and status share one right edge
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                SeverityBadge(p.row.severity)
                Text(p.row.hosts.joinToString { it.name.ifEmpty { it.host } }, color = Zw.textBody, fontSize = 12.sp, maxLines = 1,
                    overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                Text(timeShort(since), color = Zw.textSecondary, fontSize = 12.sp)
            }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.padding(top = 2.dp)) {
                Text(p.row.name, color = Zw.textPrimary, fontSize = 15.sp, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                val end = if (p.row.isOpen) now else isoMs(p.row.rClock).takeIf { it > 0 } ?: now
                Text(duration(since, end), color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(top = 2.dp))
            }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(top = 2.dp)) {
                val origin = sourceLabel(p, showServer)
                Text(origin, color = Zw.textSecondary, fontSize = 12.sp, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                if (!p.row.isOpen) StatusLabel(stringResource(R.string.state_resolved), Zw.success)
                if (p.row.suppressed) StatusLabel(stringResource(R.string.state_suppressed), Zw.textSecondary)
                if (p.row.acknowledged) StatusLabel(stringResource(R.string.state_acked), Zw.textBody)
                else if (p.row.isOpen) StatusLabel(stringResource(R.string.state_not_acked), Zw.errorText)
            }
        }
    }
    Spacer(Modifier.fillMaxWidth().height(1.dp).background(Zw.outline))
}
