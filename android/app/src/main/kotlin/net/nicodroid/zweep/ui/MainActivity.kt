// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only
//
// Zweep for Zabbix: the Android app. Made by N1k0droid (https://github.com/N1k0droid).
// Like Zweep? A star on github.com/N1k0droid/zweep and a follow help the project grow.

package net.nicodroid.zweep.ui

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.os.SystemClock
import android.view.View
import android.view.ViewTreeObserver
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.viewModels
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import net.nicodroid.zweep.BuildConfig
import net.nicodroid.zweep.R
import net.nicodroid.zweep.service.Updater

/** Where the user is */
sealed interface Route {
    data class Tabs(val tab: Int = 0) : Route
    data class Detail(val serverRef: Long, val sid: String?, val source: String, val eventId: String, val back: Route) : Route
    data class AddServer(val url: String = "", val code: String = "", val pins: String = "", val fromLink: Boolean = false, val back: Route = Tabs(2)) : Route
    data class Permissions(val back: Route) : Route
    data class About(val back: Route) : Route
}

class MainActivity : ComponentActivity() {
    private val vm: AppViewModel by viewModels()
    private var route by mutableStateOf<Route>(Route.Tabs())
    /** The update dialog appears at most once per opening of the app (activity instance) */
    private var updateAsked by mutableStateOf(false)

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        handle(intent)
        // Keep the system splash screen until the servers are loaded (at most 2 s, never stuck)
        val content = findViewById<View>(android.R.id.content)
        val start = SystemClock.uptimeMillis()
        content.viewTreeObserver.addOnPreDrawListener(object : ViewTreeObserver.OnPreDrawListener {
            override fun onPreDraw(): Boolean {
                if (vm.serversLoaded.value == null && SystemClock.uptimeMillis() - start < 2000) return false
                content.viewTreeObserver.removeOnPreDrawListener(this)
                return true
            }
        })
        setContent {
            ZweepTheme {
                // Edge-to-edge: content stays clear of the status and navigation bars and of the keyboard
                Box(Modifier.fillMaxSize().background(Zw.background).safeDrawingPadding()) {
                    App(vm, route) { route = it }
                    UpdatePrompt(vm, asked = updateAsked, onAsked = { updateAsked = true }) { route = Route.Tabs(2) }
                }
            }
        }
    }

    override fun attachBaseContext(newBase: android.content.Context) {
        super.attachBaseContext(Lang.wrap(newBase))
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handle(intent)
    }

    override fun onResume() {
        super.onResume()
        vm.resume()
    }

    private fun handle(intent: Intent?) {
        intent ?: return
        when {
            intent.action == ACTION_OPEN -> {
                val server = intent.getLongExtra(EXTRA_SERVER, 0)
                val sid = intent.getStringExtra(EXTRA_SID) ?: return
                vm.markRead(server, sid) // opening the notification ends its reminders
                route = Route.Detail(server, sid, sid.substringBefore(':'), sid.substringAfterLast(':'), Route.Tabs(0))
            }
            intent.action == Intent.ACTION_VIEW && intent.data?.scheme == "zweep" -> enrollLink(intent.data!!)?.let { route = it }
        }
    }

    /** zweep://enroll?url=…&code=…&pin=…: prefilled, never enrolled without the user's confirmation */
    private fun enrollLink(uri: Uri): Route? {
        val url = uri.getQueryParameter("url")?.trim() ?: return null
        if (!url.startsWith("https://") && !url.startsWith("http://")) return null
        return Route.AddServer(url, uri.getQueryParameter("code") ?: "", uri.getQueryParameter("pin") ?: "", fromLink = true)
    }

    companion object {
        const val ACTION_OPEN = "net.nicodroid.zweep.OPEN"
        const val EXTRA_SERVER = "server"
        const val EXTRA_SID = "sid"
    }
}

@Composable
fun App(vm: AppViewModel, route: Route, go: (Route) -> Unit) {
    // Nothing is shown until the servers are loaded, otherwise the first-run setup would flash at start
    val servers = vm.serversLoaded.collectAsStateWithLifecycle().value ?: return
    val hasProblems by vm.hasProblems.collectAsStateWithLifecycle()
    when (route) {
        is Route.Detail -> {
            BackHandler { go(route.back) }
            DetailScreen(vm, route) { go(route.back) }
        }
        is Route.AddServer -> {
            BackHandler(enabled = servers.isNotEmpty()) { go(route.back) }
            AddServerScreen(vm, route, onDone = { go(Route.Permissions(Route.Tabs(0))) }, onBack = if (servers.isNotEmpty()) { { go(route.back) } } else null)
        }
        is Route.Permissions -> {
            BackHandler { go(route.back) }
            PermissionsScreen { go(route.back) }
        }
        is Route.About -> {
            BackHandler { go(route.back) }
            AboutScreen { go(route.back) }
        }
        is Route.Tabs -> {
            if (servers.isEmpty()) {
                AddServerScreen(vm, Route.AddServer(), onDone = { go(Route.Permissions(Route.Tabs(0))) }, onBack = null)
                return
            }
            val tabs = buildList {
                add(Triple(0, R.string.tab_notifications, R.drawable.ic_notifications))
                if (hasProblems) add(Triple(1, R.string.tab_problems, R.drawable.ic_report))
                add(Triple(2, R.string.tab_settings, R.drawable.ic_settings))
            }
            val current = if (route.tab == 1 && !hasProblems) 0 else route.tab
            Scaffold(
                containerColor = Zw.background,
                contentWindowInsets = androidx.compose.foundation.layout.WindowInsets(0, 0, 0, 0),
                bottomBar = {
                    NavigationBar(containerColor = Zw.surface, windowInsets = androidx.compose.foundation.layout.WindowInsets(0, 0, 0, 0)) {
                        tabs.forEach { (id, label, icon) ->
                            NavigationBarItem(
                                selected = current == id,
                                onClick = { go(Route.Tabs(id)) },
                                icon = { Icon(painterResource(icon), contentDescription = null) },
                                label = { Text(stringResource(label)) },
                            )
                        }
                    }
                },
            ) { pad ->
                Box(Modifier.fillMaxSize().padding(pad)) {
                    when (current) {
                        0 -> NotificationsScreen(vm) { c -> go(Route.Detail(c.serverRef, c.sid, c.source, c.eventId, route)) }
                        1 -> ProblemsScreen(vm) { p -> go(Route.Detail(p.serverRef, null, p.row.source, p.row.eventid, route)) }
                        else -> SettingsScreen(
                            vm,
                            onAddServer = { go(Route.AddServer(back = route)) },
                            onPermissions = { go(Route.Permissions(route)) },
                            onAbout = { go(Route.About(route)) },
                        )
                    }
                }
            }
        }
    }
}

/** "Update available": shown [Updater.PROMPTS] times per version, then only in Settings */
@Composable
fun UpdatePrompt(vm: AppViewModel, asked: Boolean, onAsked: () -> Unit, openSettings: () -> Unit) {
    val offer = vm.updateOffer.collectAsStateWithLifecycle().value ?: return
    val servers = vm.serversLoaded.collectAsStateWithLifecycle().value
    if (servers.isNullOrEmpty()) return
    var visible by remember(offer.versionCode) { mutableStateOf(false) }
    LaunchedEffect(offer.versionCode, asked) {
        if (!asked && vm.updatePrompts(offer.versionCode) < Updater.PROMPTS) {
            onAsked()
            vm.updatePrompted(offer.versionCode)
            visible = true
        }
    }
    if (!visible) return
    AlertDialog(
        onDismissRequest = { visible = false },
        title = { Text(stringResource(R.string.update_title)) },
        text = { Text(stringResource(R.string.update_text, offer.versionName, BuildConfig.VERSION_NAME, sizeMb(offer.size))) },
        confirmButton = { TextButton({ visible = false; openSettings(); vm.startUpdate() }) { Text(stringResource(R.string.update_now)) } },
        dismissButton = { TextButton({ visible = false }) { Text(stringResource(R.string.update_later)) } },
    )
}

fun sizeMb(bytes: Long): String = String.format(java.util.Locale.ROOT, "%.1f", bytes / 1048576.0)
