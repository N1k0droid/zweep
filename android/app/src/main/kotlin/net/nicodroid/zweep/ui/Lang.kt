// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.ui

import android.app.LocaleManager
import android.content.Context
import android.content.res.Configuration
import android.os.Build
import android.os.LocaleList
import java.util.Locale

/**
 * App language: system default, English or Italian. On Android 13+ the per-app language of the
 * system is used (it applies to notifications too and shows in the system settings); below, the
 * choice is applied to the contexts of the app.
 */
object Lang {
    const val SYSTEM = ""
    val choices = listOf(SYSTEM, "en", "it")
    private const val PREFS = "lang"
    private const val KEY = "tag"

    // Device-protected: the service may start before the first unlock (Direct Boot)
    private fun prefs(ctx: Context) =
        ctx.createDeviceProtectedStorageContext().getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    fun current(ctx: Context): String {
        if (Build.VERSION.SDK_INT >= 33) {
            val l = ctx.getSystemService(LocaleManager::class.java).applicationLocales
            return if (l.isEmpty) SYSTEM else l[0].language
        }
        return prefs(ctx).getString(KEY, SYSTEM) ?: SYSTEM
    }

    fun set(ctx: Context, tag: String) {
        prefs(ctx).edit().putString(KEY, tag).apply()
        if (Build.VERSION.SDK_INT >= 33) {
            ctx.getSystemService(LocaleManager::class.java).applicationLocales =
                if (tag == SYSTEM) LocaleList.getEmptyLocaleList() else LocaleList.forLanguageTags(tag)
        }
    }

    /** Context with the chosen language (Android 12 and below) */
    fun wrap(ctx: Context): Context {
        if (Build.VERSION.SDK_INT >= 33) return ctx
        val tag = prefs(ctx).getString(KEY, SYSTEM) ?: SYSTEM
        if (tag == SYSTEM) return ctx
        val cfg = Configuration(ctx.resources.configuration)
        cfg.setLocales(LocaleList(Locale.forLanguageTag(tag)))
        return ctx.createConfigurationContext(cfg)
    }
}
