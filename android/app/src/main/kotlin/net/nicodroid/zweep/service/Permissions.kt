// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.service

import android.app.AlarmManager
import android.app.NotificationManager
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import android.os.PowerManager
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** State of the permissions Zweep depends on; also reported to the server (hello, status) */
object Permissions {
    const val LOCAL_NETWORK = "android.permission.ACCESS_LOCAL_NETWORK"

    fun notifications(ctx: Context) = ctx.getSystemService(NotificationManager::class.java).areNotificationsEnabled()

    fun batteryExempt(ctx: Context) = ctx.getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(ctx.packageName)

    fun exactAlarms(ctx: Context) = Build.VERSION.SDK_INT < 31 || ctx.getSystemService(AlarmManager::class.java).canScheduleExactAlarms()

    fun dndAccess(ctx: Context) = ctx.getSystemService(NotificationManager::class.java).isNotificationPolicyAccessGranted

    fun localNetworkNeeded() = Build.VERSION.SDK_INT >= 37

    fun localNetwork(ctx: Context) = !localNetworkNeeded() || ctx.checkSelfPermission(LOCAL_NETWORK) == PackageManager.PERMISSION_GRANTED

    /** The ones without which alarms can be missed */
    fun essentialMissing(ctx: Context) = !notifications(ctx) || !batteryExempt(ctx) || !localNetwork(ctx)

    fun json(ctx: Context): JsonObject = buildJsonObject {
        put("notifications", notifications(ctx))
        put("battery_exempt", batteryExempt(ctx))
        put("exact_alarms", exactAlarms(ctx))
        put("dnd_access", dndAccess(ctx))
        put("local_network", localNetwork(ctx))
    }
}
