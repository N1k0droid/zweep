// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.ui

import android.app.Activity
import android.content.Intent
import android.provider.Settings as AndroidSettings
import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.Icon
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilterChip
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.Lifecycle
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.layout
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlin.math.ceil
import net.nicodroid.zweep.BuildConfig
import net.nicodroid.zweep.R
import net.nicodroid.zweep.core.ChannelPrefs
import net.nicodroid.zweep.core.INFINITE
import net.nicodroid.zweep.data.ChannelEntity
import net.nicodroid.zweep.data.ServerEntity
import net.nicodroid.zweep.data.Settings
import net.nicodroid.zweep.service.ActionReceiver
import net.nicodroid.zweep.service.ConnState
import net.nicodroid.zweep.service.Engine
import net.nicodroid.zweep.service.Notifier
import net.nicodroid.zweep.service.Permissions
import net.nicodroid.zweep.service.Updater

@Composable
fun SettingsScreen(vm: AppViewModel, onAddServer: () -> Unit, onPermissions: () -> Unit, onAbout: () -> Unit) {
    val ctx = LocalContext.current
    val servers by vm.servers.collectAsStateWithLifecycle()
    val links by vm.links.collectAsStateWithLifecycle()
    val channels by vm.channels.collectAsStateWithLifecycle()
    val settings by vm.settings.collectAsStateWithLifecycle()
    val max = settings[Settings.MAX_SERVERS]?.toIntOrNull() ?: 1
    val dnd = settings[Settings.DND_UNTIL]?.toLongOrNull() ?: 0
    var confirmQuit by remember { mutableStateOf(false) }
    var confirmLogout by remember { mutableStateOf<ServerEntity?>(null) }
    var maxMenu by remember { mutableStateOf(false) }
    var info by remember { mutableStateOf<String?>(null) }
    var confirmRestore by remember { mutableStateOf<ChannelEntity?>(null) }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    var resumes by remember { mutableIntStateOf(0) }
    DisposableEffect(lifecycle) {
        val obs = LifecycleEventObserver { _, e -> if (e == Lifecycle.Event.ON_RESUME) resumes++ }
        lifecycle.addObserver(obs)
        onDispose { lifecycle.removeObserver(obs) }
    }

    Column(Modifier.fillMaxSize()) {
        Header(stringResource(R.string.tab_settings))
        StatusStrip(vm)
        Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState())) {
            UpdateCard(vm)
            SectionTitle(stringResource(R.string.settings_servers))
            Row(Modifier.padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(stringResource(R.string.settings_max_servers, max), color = Zw.textBody, modifier = Modifier.weight(1f))
                Column {
                    TextButton({ maxMenu = true }) { Text(stringResource(R.string.action_change)) }
                    DropdownMenu(maxMenu, { maxMenu = false }) {
                        (1..Settings.LIMIT_SERVERS).forEach { n ->
                            DropdownMenuItem({ Text(n.toString()) }, { vm.setMaxServers(n.coerceAtLeast(servers.size)); maxMenu = false })
                        }
                    }
                }
            }
            Text(stringResource(R.string.settings_max_servers_hint), color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(horizontal = 16.dp))
            servers.forEach { s ->
                ServerCard(s, links[s.id]?.state, links[s.id]?.detail,
                    onTest = { vm.testAlarm(s.id, 4) { ok -> info = ctx.getString(if (ok) R.string.test_sent else R.string.test_failed) } },
                    onLogout = { confirmLogout = s })
            }
            TextButton(onClick = onAddServer, enabled = servers.size < max, modifier = Modifier.padding(horizontal = 8.dp)) {
                Text(if (servers.size < max) stringResource(R.string.settings_add_server) else stringResource(R.string.settings_limit_reached))
            }

            SectionDivider()
            SectionTitle(stringResource(R.string.settings_language))
            var lang by remember { mutableStateOf(Lang.current(ctx)) }
            Row(Modifier.padding(horizontal = 8.dp), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                Lang.choices.forEach { tag ->
                    FilterChip(lang == tag, {
                        lang = tag
                        Lang.set(ctx, tag)
                        if (android.os.Build.VERSION.SDK_INT < 33) (ctx as? Activity)?.recreate()
                    }, label = {
                        Text(stringResource(when (tag) { "en" -> R.string.lang_en; "it" -> R.string.lang_it; else -> R.string.lang_system }))
                    })
                }
            }

            SectionDivider()
            SectionTitle(stringResource(R.string.settings_permissions))
            val missing = Permissions.essentialMissing(ctx)
            Row(Modifier.padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(stringResource(if (missing) R.string.permissions_missing else R.string.permissions_ok), color = if (missing) Zw.warningText else Zw.success,
                    modifier = Modifier.weight(1f))
                TextButton(onPermissions) { Text(stringResource(R.string.action_manage)) }
            }

            SectionDivider()
            SectionTitle(stringResource(R.string.settings_channels))
            servers.forEach { s ->
                val list = channels.filter { it.serverRef == s.id }
                if (servers.size > 1) Text(s.label, color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(horizontal = 16.dp))
                list.forEach { c ->
                    // Read again when the screen comes back from the Android settings of the channel
                    val zweepSound = remember(resumes, c.serverRef, c.id) { Engine.notifier.usesZweepSound(c.serverRef, c.id) }
                    ChannelRow(c, onChange = vm::updateChannel, onSystem = {
                        ctx.startActivity(Intent(AndroidSettings.ACTION_CHANNEL_NOTIFICATION_SETTINGS).putExtra(AndroidSettings.EXTRA_APP_PACKAGE, ctx.packageName)
                            .putExtra(AndroidSettings.EXTRA_CHANNEL_ID, Engine.notifier.channelFor(c.serverRef, c.id)))
                    }, onRestore = if (zweepSound) null else { { confirmRestore = c } })
                }
                // Recoveries: their own sound, changeable, with the Zweep sound restorable
                val resolvedZweep = remember(resumes, s.id) { Engine.notifier.usesZweepSound(s.id, Notifier.RESOLVED) }
                Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 4.dp)) {
                    Text(stringResource(R.string.channel_resolved), color = Zw.textPrimary)
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        TextButton({
                            ctx.startActivity(Intent(AndroidSettings.ACTION_CHANNEL_NOTIFICATION_SETTINGS).putExtra(AndroidSettings.EXTRA_APP_PACKAGE, ctx.packageName)
                                .putExtra(AndroidSettings.EXTRA_CHANNEL_ID, Engine.notifier.channelFor(s.id, Notifier.RESOLVED)))
                        }) { Text(stringResource(R.string.channel_sound)) }
                        if (!resolvedZweep) TextButton({ vm.restoreResolvedSound(s.id) }) { Text(stringResource(R.string.sound_restore)) }
                    }
                }
            }

            SectionDivider()
            SectionTitle(stringResource(R.string.settings_dnd))
            val now = System.currentTimeMillis()
            if (dnd > now) {
                Row(Modifier.padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(stringResource(R.string.dnd_active_until, timeShort(dnd)), color = Zw.warningText, modifier = Modifier.weight(1f))
                    TextButton({ vm.setDnd(0) }) { Text(stringResource(R.string.dnd_off)) }
                }
            } else {
                Row(Modifier.padding(horizontal = 8.dp), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                    Settings.dndChoicesMinutes.forEach { m -> TextButton({ vm.setDnd(now + m * 60_000L) }) { Text(if (m < 60) "${m}m" else "${m / 60}h") } }
                    TextButton({ vm.setDnd(ActionReceiver.untilEight(now)) }) { Text(stringResource(R.string.dnd_until_8)) }
                }
            }
            Text(stringResource(R.string.dnd_hint), color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(horizontal = 16.dp))

            SectionDivider()
            SectionTitle(stringResource(R.string.settings_data))
            Text(stringResource(R.string.settings_resolved_visible), color = Zw.textBody, modifier = Modifier.padding(horizontal = 16.dp))
            val resolved = settings[Settings.RESOLVED_MINUTES]?.toIntOrNull() ?: Settings.DEFAULT_RESOLVED_MINUTES
            Row(Modifier.padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Settings.resolvedChoicesMinutes.forEach { m ->
                    FilterChip(resolved == m, { vm.setSetting(Settings.RESOLVED_MINUTES, m.toString()) },
                        label = { Text(if (m < 60) "$m min" else if (m < 1440) "${m / 60} h" else "${m / 60} h") })
                }
            }
            Text(stringResource(R.string.settings_history), color = Zw.textBody, modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp))
            val kept = Settings.localHistoryMinutes(settings[Settings.HISTORY_MINUTES], settings[Settings.HISTORY_DAYS])
            Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Settings.problemHistoryChoicesMinutes.forEach { m ->
                    FilterChip(kept == m, { vm.setHistoryMinutes(m) }, label = { Text(periodLabel(m)) })
                }
            }
            Text(stringResource(R.string.settings_history_hint), color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(horizontal = 16.dp))
            if (vm.hasProblemViews.collectAsStateWithLifecycle().value) {
                Text(stringResource(R.string.settings_problem_history), color = Zw.textBody, modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp))
                val period = Settings.problemHistoryMinutes(settings[Settings.PROBLEM_HISTORY_MINUTES])
                Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Settings.problemHistoryChoicesMinutes.forEach { m ->
                        FilterChip(period == m, { vm.setSetting(Settings.PROBLEM_HISTORY_MINUTES, m.toString()) }, label = { Text(periodLabel(m)) })
                    }
                }
                Text(stringResource(R.string.settings_problem_history_hint), color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(horizontal = 16.dp))
            }

            SectionDivider()
            UpdateSection(vm)

            SectionDivider()
            Row(Modifier.fillMaxWidth().padding(16.dp), horizontalArrangement = Arrangement.spacedBy(40.dp, Alignment.CenterHorizontally)) {
                OutlinedButton({ confirmQuit = true }) { Text(stringResource(R.string.action_quit), color = Zw.errorText) }
                OutlinedButton(onAbout) { Text(stringResource(R.string.about_title)) }
            }
            Text(stringResource(R.string.independent_notice), color = Zw.textSecondary, fontSize = 12.sp, textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth().padding(16.dp))
        }
    }

    if (confirmQuit) {
        AlertDialog(onDismissRequest = { confirmQuit = false },
            title = { Text(stringResource(R.string.action_quit)) },
            text = { Text(stringResource(R.string.quit_confirm)) },
            confirmButton = { TextButton({ confirmQuit = false; vm.quit { (ctx as? Activity)?.finishAndRemoveTask() } }) { Text(stringResource(R.string.action_quit), color = Zw.errorText) } },
            dismissButton = { TextButton({ confirmQuit = false }) { Text(stringResource(R.string.action_cancel)) } })
    }
    confirmLogout?.let { s ->
        AlertDialog(onDismissRequest = { confirmLogout = null },
            title = { Text(stringResource(R.string.action_logout)) },
            text = { Text(stringResource(R.string.logout_confirm, s.label)) },
            confirmButton = { TextButton({ confirmLogout = null; vm.logout(s.id) }) { Text(stringResource(R.string.action_logout), color = Zw.errorText) } },
            dismissButton = { TextButton({ confirmLogout = null }) { Text(stringResource(R.string.action_cancel)) } })
    }
    confirmRestore?.let { c ->
        AlertDialog(onDismissRequest = { confirmRestore = null },
            title = { Text(stringResource(R.string.sound_restore)) },
            text = { Text(stringResource(R.string.sound_restore_confirm)) },
            confirmButton = { TextButton({ Engine.notifier.restoreZweepSound(c.serverRef, c); confirmRestore = null; resumes++ }) { Text(stringResource(R.string.sound_restore)) } },
            dismissButton = { TextButton({ confirmRestore = null }) { Text(stringResource(R.string.action_cancel)) } })
    }
    info?.let { msg ->
        AlertDialog(onDismissRequest = { info = null }, text = { Text(msg) }, confirmButton = { TextButton({ info = null }) { Text("OK") } })
    }
}

@Composable
private fun ServerCard(s: ServerEntity, state: ConnState?, detail: String?, onTest: () -> Unit, onLogout: () -> Unit) {
    val (security, secColor) = when {
        s.baseUrl.startsWith("http://") -> stringResource(R.string.security_cleartext) to Zw.warningText
        s.pins.isNotEmpty() -> stringResource(R.string.security_pinned) to Zw.success
        else -> stringResource(R.string.security_tls) to Zw.success
    }
    val (conn, connColor) = when (state) {
        ConnState.CONNECTED -> stringResource(R.string.conn_connected) to Zw.success
        ConnState.CONNECTING -> stringResource(R.string.conn_connecting) to Zw.warningText
        ConnState.REJECTED -> (if (detail == "revoked" || detail == "unauthorized") stringResource(R.string.conn_revoked) else stringResource(R.string.conn_rejected, detail ?: "")) to Zw.errorText
        ConnState.WAITING -> stringResource(R.string.conn_reconnecting) to Zw.warningText
        else -> stringResource(R.string.conn_disconnected) to Zw.textSecondary
    }
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 6.dp).background(Zw.surface).padding(12.dp)) {
        // Same start and end as the text of a TextButton (its content padding), so the lines align with "Log out"
        val padStart = ButtonDefaults.TextButtonContentPadding.calculateLeftPadding(LayoutDirection.Ltr)
        val padEnd = ButtonDefaults.TextButtonContentPadding.calculateRightPadding(LayoutDirection.Ltr)
        Row(verticalAlignment = Alignment.Top) {
            Column(Modifier.weight(1f).padding(start = padStart)) {
                Text(s.label, color = Zw.textPrimary, fontWeight = FontWeight.Bold)
                Text(s.baseUrl, color = Zw.textSecondary, fontSize = 12.sp)
                Text(security, color = secColor, fontSize = 12.sp)
            }
            TextButton(onLogout, Modifier.offset(y = (-8).dp)) { Text(stringResource(R.string.action_logout), color = Zw.errorText) }
        }
        // Account on the left, connection state on the right, on one line
        Row(Modifier.fillMaxWidth().padding(start = padStart, end = padEnd), verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(R.string.settings_account, s.username), color = Zw.textBody, fontSize = 12.sp, modifier = Modifier.weight(1f))
            Text(stringResource(R.string.settings_status), color = Zw.textBody, fontSize = 12.sp)
            Text(" $conn", color = connColor, fontSize = 12.sp)
        }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.Center) {
            OutlinedButton(onTest, Modifier.padding(top = 8.dp)) { Text(stringResource(R.string.action_test_alarm)) }
        }
    }
}

@Composable
private fun ChannelRow(c: ChannelEntity, onChange: (ChannelEntity) -> Unit, onSystem: () -> Unit, onRestore: (() -> Unit)? = null) {
    var remMenu by remember { mutableStateOf(false) }
    var intMenu by remember { mutableStateOf(false) }
    val sev = Notifier.severityOf(c.id)
    val name = sev?.let { stringResource(Notifier.severityLabel(it)) } ?: c.name
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 4.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(name, color = if (c.serverEnabled) Zw.textPrimary else Zw.textSecondary)
                    if (sev == null) ChannelDot(c.color, Modifier.padding(start = 8.dp))
                }
                if (!c.serverEnabled) Text(stringResource(R.string.channel_disabled_by_admin), color = Zw.textSecondary, fontSize = 11.sp)
            }
            Switch(c.notify, { onChange(c.copy(notify = it)) }, enabled = c.serverEnabled)
        }
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column {
                TextButton({ remMenu = true }) { Text(stringResource(R.string.channel_reminders, reminderText(c.reminders))) }
                DropdownMenu(remMenu, { remMenu = false }) {
                    ChannelPrefs.reminderChoices.forEach { n -> DropdownMenuItem({ Text(reminderText(n)) }, { onChange(c.copy(reminders = n)); remMenu = false }) }
                }
            }
            if (c.reminders != 0) {
                Column {
                    TextButton({ intMenu = true }) { Text(stringResource(R.string.channel_interval, c.reminderMinutes)) }
                    DropdownMenu(intMenu, { intMenu = false }) {
                        ChannelPrefs.intervalChoices.forEach { m -> DropdownMenuItem({ Text("$m min") }, { onChange(c.copy(reminderMinutes = m)); intMenu = false }) }
                    }
                }
            }
            TextButton(onSystem) { Text(stringResource(R.string.channel_sound)) }
        }
        if (onRestore != null) {
            TextButton(onRestore) { Text(stringResource(R.string.sound_restore)) }
        }
    }
}

@Composable
private fun reminderText(n: Int) = if (n == INFINITE) "∞" else n.toString()

/** Update offered by a server, at the top of Settings while it is available */
@Composable
private fun UpdateCard(vm: AppViewModel) {
    val offer = vm.updateOffer.collectAsStateWithLifecycle().value ?: return
    Column(Modifier.fillMaxWidth().padding(16.dp)) {
        Text(stringResource(R.string.update_title), color = Zw.accent, fontWeight = FontWeight.Bold)
        UpdateBody(vm, offer)
    }
    SectionDivider()
}

/** Section "Update": installed version, manual check, and the offer if any */
@Composable
private fun UpdateSection(vm: AppViewModel) {
    val offer = vm.updateOffer.collectAsStateWithLifecycle().value
    val check by vm.updateCheck.collectAsStateWithLifecycle()
    SectionTitle(stringResource(R.string.settings_update))
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp)) {
        Text(stringResource(R.string.update_installed, BuildConfig.VERSION_NAME, BuildConfig.VERSION_CODE), color = Zw.textBody, fontSize = 14.sp)
        if (offer != null) {
            UpdateBody(vm, offer)
        } else {
            when (check) {
                AppViewModel.UpdateCheck.Checking -> Text(stringResource(R.string.update_checking), color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(top = 4.dp))
                AppViewModel.UpdateCheck.UpToDate -> Text(stringResource(R.string.update_up_to_date), color = Zw.success, fontSize = 12.sp, modifier = Modifier.padding(top = 4.dp))
                is AppViewModel.UpdateCheck.Failed -> Text(stringResource(R.string.update_check_failed, (check as AppViewModel.UpdateCheck.Failed).servers), color = Zw.errorText, fontSize = 12.sp, modifier = Modifier.padding(top = 4.dp))
                null -> Unit
            }
        }
        FilterChip(false, { vm.checkUpdates() }, enabled = check != AppViewModel.UpdateCheck.Checking, modifier = Modifier.padding(top = 4.dp),
            label = { Text(stringResource(R.string.update_check)) })
    }
}

/** Version, download progress, installation (confirmed by Android) of an offered update */
@Composable
private fun UpdateBody(vm: AppViewModel, offer: Updater.Offer) {
    val ctx = LocalContext.current
    val state = vm.updateState.collectAsStateWithLifecycle().value
    val confirm = vm.updateConfirm.collectAsStateWithLifecycle().value
    Column {
        Text(stringResource(R.string.update_text, offer.versionName, BuildConfig.VERSION_NAME, sizeMb(offer.size)), color = Zw.textBody, fontSize = 14.sp)
        when (state) {
            is Updater.State.Downloading -> {
                Text(stringResource(R.string.update_downloading, state.percent), color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(top = 8.dp))
                LinearProgressIndicator(progress = { state.percent / 100f }, modifier = Modifier.fillMaxWidth().padding(top = 4.dp))
            }
            Updater.State.Installing -> {
                Text(stringResource(R.string.update_installing), color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(top = 8.dp))
                // The confirmation may not have opened (Android, or dismissed): open it, or start again
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    if (confirm != null) TextButton({ vm.openUpdateConfirmation(ctx) }) { Text(stringResource(R.string.update_open_confirmation)) }
                    TextButton({ vm.resetUpdate(); vm.startUpdate() }) { Text(stringResource(R.string.update_retry)) }
                }
            }
            Updater.State.NeedsPermission -> {
                Text(stringResource(R.string.update_permission), color = Zw.warningText, fontSize = 12.sp, modifier = Modifier.padding(top = 8.dp))
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    TextButton({ ctx.startActivity(Updater.permissionIntent(ctx)) }) { Text(stringResource(R.string.action_grant)) }
                    TextButton({ vm.resetUpdate(); vm.startUpdate() }) { Text(stringResource(R.string.update_now)) }
                }
            }
            is Updater.State.Failed -> {
                Text(stringResource(R.string.update_failed, state.reason), color = Zw.errorText, fontSize = 12.sp, modifier = Modifier.padding(top = 8.dp))
                TextButton({ vm.resetUpdate(); vm.startUpdate() }) { Text(stringResource(R.string.update_retry)) }
            }
            Updater.State.Idle -> TextButton({ vm.startUpdate() }) { Text(stringResource(R.string.update_now)) }
        }
        Text(stringResource(R.string.update_hint), color = Zw.textSecondary, fontSize = 12.sp)
    }
}
