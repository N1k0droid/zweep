// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.service

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.net.Uri
import android.os.Build
import android.provider.Settings as AndroidSettings
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import net.nicodroid.zweep.BuildConfig
import net.nicodroid.zweep.core.WireJson
import net.nicodroid.zweep.data.SettingEntity
import net.nicodroid.zweep.net.AppUpdate
import java.io.File

/**
 * Updates of the app offered by a Zweep server (newest APK of its directory). The offer arrives with
 * the configuration; the APK is downloaded on the authenticated connection of that server (same
 * address, same certificate check), its SHA-256 checked, then handed to the Android installer, which
 * asks the user to confirm and installs it only when it is signed with the same key.
 */
object Updater {
    /** The best offer among the servers, stored as JSON in the settings */
    @Serializable
    data class Offer(val serverRef: Long, val versionCode: Long, val versionName: String, val size: Long, val sha256: String, val path: String)

    sealed interface State {
        data object Idle : State
        data class Downloading(val percent: Int) : State
        data object Installing : State
        data object NeedsPermission : State
        data class Failed(val reason: String) : State
    }

    const val OFFER = "update_offer"
    private const val SEEN = "update_seen_"
    /** How many times the update dialog appears for a version; then only Settings shows it */
    const val PROMPTS = 3

    private val _state = MutableStateFlow<State>(State.Idle)
    val state: StateFlow<State> = _state

    /**
     * The Android confirmation of the installation, when it could not be opened by itself (Android may
     * refuse to open a window from the background): the app opens it from its own screen.
     */
    private val _confirm = MutableStateFlow<Intent?>(null)
    val confirm: StateFlow<Intent?> = _confirm

    fun parse(json: String?): Offer? = json?.let { runCatching { WireJson.decodeFromString(Offer.serializer(), it) }.getOrNull() }
        ?.takeIf { it.versionCode > BuildConfig.VERSION_CODE }

    fun seenKey(code: Long) = SEEN + code

    /** Records what a server offers (called after each configuration fetch) */
    suspend fun offer(serverRef: Long, up: AppUpdate?) {
        val dao = Engine.db.settings()
        val cur = dao.get(OFFER)?.let { runCatching { WireJson.decodeFromString(Offer.serializer(), it) }.getOrNull() }
        val newer = up != null && up.versionCode > BuildConfig.VERSION_CODE
        when {
            newer && (cur == null || cur.serverRef == serverRef || up!!.versionCode > cur.versionCode) ->
                dao.put(SettingEntity(OFFER, WireJson.encodeToString(Offer.serializer(),
                    Offer(serverRef, up!!.versionCode, up.versionName, up.size, up.sha256, up.path))))
            // This server withdrew its offer, or the app is already up to date
            cur != null && (cur.serverRef == serverRef || cur.versionCode <= BuildConfig.VERSION_CODE) ->
                dao.put(SettingEntity(OFFER, ""))
        }
    }

    suspend fun prompted(code: Long) {
        val dao = Engine.db.settings()
        val n = dao.get(seenKey(code))?.toIntOrNull() ?: 0
        dao.put(SettingEntity(seenKey(code), (n + 1).toString()))
    }

    /** Downloads, checks and installs the offered APK; the progress is in [state] */
    suspend fun install(ctx: Context) = withContext(Dispatchers.IO) {
        // A download in progress is not started twice; an installation left waiting (confirmation never
        // shown or dismissed) is started again from the beginning
        if (_state.value is State.Downloading) return@withContext
        _confirm.value = null
        val offer = parse(Engine.db.settings().get(OFFER)) ?: return@withContext
        if (!ctx.packageManager.canRequestPackageInstalls()) {
            _state.value = State.NeedsPermission
            return@withContext
        }
        val server = Engine.db.servers().get(offer.serverRef)
        if (server == null) {
            _state.value = State.Failed("server removed")
            return@withContext
        }
        val file = File(ctx.cacheDir, "zweep-update.apk")
        try {
            _state.value = State.Downloading(0)
            val sha = Engine.api(server).download(offer.path, file) { done, total ->
                _state.value = State.Downloading(if (total > 0) (done * 100 / total).toInt() else 0)
            }
            if (!sha.equals(offer.sha256, ignoreCase = true)) throw java.io.IOException("checksum mismatch")
            _state.value = State.Installing
            commit(ctx, file)
        } catch (t: Throwable) {
            Engine.warn("update", t)
            _state.value = State.Failed(t.message?.take(120) ?: t.javaClass.simpleName)
        } finally {
            file.delete()
        }
    }

    private fun commit(ctx: Context, file: File) {
        val installer = ctx.packageManager.packageInstaller
        // Sessions of an earlier attempt that never completed
        installer.mySessions.forEach { runCatching { installer.abandonSession(it.sessionId) } }
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(ctx.packageName)
            setSize(file.length())
        }
        val id = installer.createSession(params)
        installer.openSession(id).use { session ->
            session.openWrite("zweep.apk", 0, file.length()).use { out ->
                file.inputStream().use { it.copyTo(out) }
                session.fsync(out)
            }
            val intent = Intent(ctx, InstallReceiver::class.java).setPackage(ctx.packageName)
            // The installer adds the result to the intent: it must be mutable
            val pending = PendingIntent.getBroadcast(ctx, id, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE)
            session.commit(pending.intentSender)
        }
    }

    /** Opens the system page where the user allows Zweep to install updates */
    fun permissionIntent(ctx: Context): Intent =
        Intent(AndroidSettings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${ctx.packageName}")).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)

    fun reset() {
        _state.value = State.Idle
        _confirm.value = null
    }

    internal fun needsConfirmation(intent: Intent) {
        _confirm.value = intent
    }

    /** Opens the Android confirmation from an activity (always allowed in the foreground) */
    fun openConfirmation(ctx: Context) {
        val i = _confirm.value ?: return
        runCatching { ctx.startActivity(i) }.onFailure { Engine.warn("update confirmation", it) }
    }

    internal fun result(status: Int, message: String?) {
        _confirm.value = null
        _state.value = when (status) {
            PackageInstaller.STATUS_SUCCESS -> State.Idle
            PackageInstaller.STATUS_FAILURE_ABORTED -> State.Idle // the user said no
            else -> State.Failed(message?.take(120) ?: "installer status $status")
        }
    }
}

/** Result of the installer session: the confirmation to show, or the outcome */
class InstallReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)
        if (status == PackageInstaller.STATUS_PENDING_USER_ACTION) {
            val confirm = if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
            else @Suppress("DEPRECATION") intent.getParcelableExtra(Intent.EXTRA_INTENT)
            confirm?.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)?.let {
                Updater.needsConfirmation(it) // kept, in case Android does not open it from here
                runCatching { ctx.startActivity(it) }.onFailure { e -> Engine.warn("update confirmation", e) }
            }
            return
        }
        Updater.result(status, intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE))
    }
}
