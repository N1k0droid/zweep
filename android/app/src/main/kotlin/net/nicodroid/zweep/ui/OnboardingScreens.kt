// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.ui

import android.Manifest
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings as AndroidSettings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.FilterChip
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.foundation.clickable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LifecycleEventEffect
import net.nicodroid.zweep.BuildConfig
import net.nicodroid.zweep.R
import net.nicodroid.zweep.service.Engine
import net.nicodroid.zweep.service.Permissions

@Composable
fun AddServerScreen(vm: AppViewModel, r: Route.AddServer, onDone: () -> Unit, onBack: (() -> Unit)?) {
    var url by remember(r) { mutableStateOf(r.url) }
    var usePassword by remember(r) { mutableStateOf(r.code.isEmpty() && !r.fromLink) }
    var code by remember(r) { mutableStateOf(r.code) }
    var username by remember { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    var pins by remember(r) { mutableStateOf(r.pins) }
    var cleartextOk by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val ctx = LocalContext.current

    val normalized = url.trim().let { if (it.isEmpty() || it.startsWith("http://") || it.startsWith("https://")) it else "https://$it" }
    val cleartext = normalized.startsWith("http://")
    val ready = normalized.isNotEmpty() && !busy && (!cleartext || cleartextOk) &&
        (if (usePassword) username.isNotBlank() && password.isNotEmpty() else code.isNotBlank())

    Column(Modifier.fillMaxSize()) {
        Header(stringResource(R.string.add_server_title), onBack)
        Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            if (onBack == null) {
                Text(stringResource(R.string.app_name), color = Zw.textPrimary, fontSize = 28.sp, fontWeight = FontWeight.Bold)
                Text(stringResource(R.string.app_tagline), color = Zw.textSecondary)
            }
            if (r.fromLink) {
                // A QR code can point anywhere: the user must recognize the address before enrolling
                Text(stringResource(R.string.add_server_link_warning, normalized), color = Zw.warningText, fontSize = 14.sp,
                    modifier = Modifier.fillMaxWidth().background(Zw.surface).padding(12.dp))
            }
            OutlinedTextField(url, { url = it }, Modifier.fillMaxWidth(), label = { Text(stringResource(R.string.server_url_label)) },
                supportingText = { Text(stringResource(R.string.server_url_hint)) }, singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri))
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                FilterChip(!usePassword, { usePassword = false }, label = { Text(stringResource(R.string.add_server_by_code)) })
                FilterChip(usePassword, { usePassword = true }, label = { Text(stringResource(R.string.add_server_by_password)) })
            }
            if (usePassword) {
                OutlinedTextField(username, { username = it }, Modifier.fillMaxWidth(), label = { Text(stringResource(R.string.settings_username)) }, singleLine = true)
                OutlinedTextField(password, { password = it }, Modifier.fillMaxWidth(), label = { Text(stringResource(R.string.password)) }, singleLine = true,
                    visualTransformation = PasswordVisualTransformation(), keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password))
            } else {
                if (!r.fromLink) {
                    // Zweep has no camera permission: the phone camera reads the QR code and opens the zweep:// link
                    Text(stringResource(R.string.add_server_qr_hint), color = Zw.textSecondary, fontSize = 14.sp,
                        modifier = Modifier.fillMaxWidth().background(Zw.surface).padding(12.dp))
                }
                OutlinedTextField(code, { code = it.trim() }, Modifier.fillMaxWidth(), label = { Text(stringResource(R.string.add_server_code)) }, singleLine = true)
            }
            OutlinedTextField(pins, { pins = it.trim() }, Modifier.fillMaxWidth(), label = { Text(stringResource(R.string.add_server_pin)) },
                supportingText = { Text(stringResource(R.string.add_server_pin_hint)) }, singleLine = true)
            if (cleartext) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(cleartextOk, { cleartextOk = it })
                    Text(stringResource(R.string.add_server_cleartext), color = Zw.warningText, fontSize = 13.sp)
                }
            }
            error?.let { Text(it, color = Zw.errorText, fontSize = 13.sp) }
            Button(enabled = ready, onClick = {
                busy = true
                error = null
                val pinList = pins.split(',').map { it.trim() }.filter { it.isNotEmpty() }
                vm.enroll(normalized, if (usePassword) null else code, username.takeIf { usePassword }, password.takeIf { usePassword }, pinList, cleartext) { res ->
                    busy = false
                    when (res) {
                        is Engine.EnrollResult.Ok -> onDone()
                        is Engine.EnrollResult.Failed -> error = ctx.getString(
                            when (res.reason) {
                                "max_servers" -> R.string.enroll_max_servers
                                "already_added" -> R.string.enroll_already_added
                                "enrollment_invalid", "unauthorized", "invalid_code" -> R.string.enroll_invalid
                                "too_many_requests", "too_many_auth_failures" -> R.string.enroll_too_many
                                else -> R.string.enroll_failed
                            },
                        ) + if (res.reason.startsWith("network")) "\n" + ctx.getString(R.string.error_unreachable_hint) else ""
                    }
                }
            }) { Text(stringResource(if (busy) R.string.conn_connecting else R.string.action_connect)) }
        }
    }
}

@Composable
fun PermissionsScreen(onDone: () -> Unit) {
    val ctx = LocalContext.current
    var tick by remember { mutableIntStateOf(0) }
    LifecycleEventEffect(Lifecycle.Event.ON_RESUME) { tick++ }
    val notifLauncher = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { tick++ }
    @Suppress("UNUSED_EXPRESSION") tick
    Column(Modifier.fillMaxSize()) {
        Header(stringResource(R.string.settings_permissions), onDone)
        Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text(stringResource(R.string.permissions_intro), color = Zw.textBody, fontSize = 14.sp)
            PermissionRow(R.string.perm_notifications, R.string.perm_notifications_why, Permissions.notifications(ctx), required = true) {
                if (Build.VERSION.SDK_INT >= 33) notifLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
                else ctx.startActivity(Intent(AndroidSettings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(AndroidSettings.EXTRA_APP_PACKAGE, ctx.packageName))
            }
            PermissionRow(R.string.perm_battery, R.string.perm_battery_why, Permissions.batteryExempt(ctx), required = true) {
                @Suppress("BatteryLife")
                ctx.startActivity(Intent(AndroidSettings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:${ctx.packageName}")))
            }
            PermissionRow(R.string.perm_exact_alarms, R.string.perm_exact_alarms_why, Permissions.exactAlarms(ctx), required = false) {
                if (Build.VERSION.SDK_INT >= 31) ctx.startActivity(Intent(AndroidSettings.ACTION_REQUEST_SCHEDULE_EXACT_ALARM, Uri.parse("package:${ctx.packageName}")))
            }
            if (Permissions.localNetworkNeeded()) {
                PermissionRow(R.string.perm_local_network, R.string.perm_local_network_why, Permissions.localNetwork(ctx), required = true) {
                    notifLauncher.launch(Permissions.LOCAL_NETWORK)
                }
            }
            PermissionRow(R.string.perm_dnd, R.string.perm_dnd_why, Permissions.dndAccess(ctx), required = false) {
                ctx.startActivity(Intent(AndroidSettings.ACTION_NOTIFICATION_POLICY_ACCESS_SETTINGS))
            }
            SectionTitle(inset = false, text = stringResource(R.string.vendor_title))
            Text(stringResource(R.string.vendor_text, Build.MANUFACTURER), color = Zw.textBody, fontSize = 13.sp)
            TextButton({ openInBrowser(ctx, "https://dontkillmyapp.com/" + Build.MANUFACTURER.lowercase().replace(" ", "-")) }) {
                Text(stringResource(R.string.vendor_open))
            }
            Button(onDone) { Text(stringResource(R.string.action_done)) }
        }
    }
}

@Composable
private fun PermissionRow(title: Int, why: Int, granted: Boolean, required: Boolean, request: () -> Unit) {
    Column(Modifier.fillMaxWidth().background(Zw.surface).padding(12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(title), color = Zw.textPrimary, fontWeight = FontWeight.SemiBold, modifier = Modifier.weight(1f))
            Text(
                stringResource(if (granted) R.string.perm_granted else if (required) R.string.perm_missing_required else R.string.perm_missing_optional),
                color = if (granted) Zw.success else if (required) Zw.errorText else Zw.textSecondary, fontSize = 12.sp,
            )
        }
        Text(stringResource(why), color = Zw.textSecondary, fontSize = 12.sp)
        if (!granted) TextButton(request) { Text(stringResource(R.string.action_grant)) }
    }
}

@Composable
fun AboutScreen(onBack: () -> Unit) {
    val ctx = LocalContext.current
    Column(Modifier.fillMaxSize()) {
        Header(stringResource(R.string.about_title), onBack)
        Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("Zweep ${BuildConfig.VERSION_NAME}", color = Zw.textPrimary, fontSize = 20.sp, fontWeight = FontWeight.Bold)
            Text(stringResource(R.string.app_tagline), color = Zw.textSecondary)
            SectionTitle(inset = false, text = stringResource(R.string.about_author))
            Text(stringResource(R.string.about_author_text), color = Zw.textBody, fontSize = 13.sp)
            // Name and profile link close together, centered, with the same space above and below
            Column(Modifier.fillMaxWidth().padding(vertical = 12.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                Text(AUTHOR_NAME, color = Zw.textPrimary, fontSize = 15.sp, fontWeight = FontWeight.Bold)
                Text("github.com/N1k0droid", color = Zw.accent, fontSize = 14.sp,
                    modifier = Modifier.clickable { openInBrowser(ctx, AUTHOR_URL) }.padding(top = 2.dp))
            }
            Text(stringResource(R.string.about_star), color = Zw.textSecondary, fontSize = 13.sp, textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth())
            SectionTitle(inset = false, text = stringResource(R.string.about_license))
            Text(stringResource(R.string.about_license_text), color = Zw.textBody, fontSize = 13.sp)
            // AGPL-3.0 section 13: the source of this very build
            if (BuildConfig.SOURCE_URL.isNotEmpty()) {
                TextButton({ openInBrowser(ctx, BuildConfig.SOURCE_URL) }) { Text(stringResource(R.string.about_source, BuildConfig.SOURCE_URL)) }
            } else {
                Text(stringResource(R.string.about_source_missing), color = Zw.warningText, fontSize = 13.sp)
            }
            SectionTitle(inset = false, text = stringResource(R.string.about_third_party))
            Text(stringResource(R.string.about_third_party_text), color = Zw.textBody, fontSize = 13.sp)
            SectionTitle(inset = false, text = stringResource(R.string.about_trademark))
            Text(stringResource(R.string.trademark_notice), color = Zw.textSecondary, fontSize = 12.sp)
        }
    }
}

/** The author of Zweep (About page) */
const val AUTHOR_NAME = "Nicola Carmelo Gurgone"
const val AUTHOR_URL = "https://github.com/N1k0droid"
