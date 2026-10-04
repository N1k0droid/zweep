// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.ui

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color

/** Design tokens of the Zweep visual identity */
object Zw {
    val background = Color(0xFF0F172A)
    val surface = Color(0xFF1E293B)
    val outline = Color(0xFF334155)
    val outlineStrong = Color(0xFF475569)
    val textPrimary = Color(0xFFFFFFFF)
    val textBody = Color(0xFFCBD5E1)
    val textSecondary = Color(0xFF94A3B8)
    val accent = Color(0xFF3B82F6)
    val brand = Color(0xFFD32F2E) // fills and accents only (text on dark fails AA)
    val success = Color(0xFF22C55E)
    val warning = Color(0xFFF59E0B)
    val warningText = Color(0xFFFBBF24)
    val error = Color(0xFFEF4444)
    val errorText = Color(0xFFFCA5A5)
    val onSeverity = Color(0xFF0F172A) // text on severity fills: never white

    fun severity(sev: Int) = when (sev.coerceAtLeast(0)) {
        0 -> Color(0xFF97AAB3)
        1 -> Color(0xFF7499FF)
        2 -> Color(0xFFFFC859)
        3 -> Color(0xFFFFA059)
        4 -> Color(0xFFE97659)
        else -> Color(0xFFE45959)
    }
}

@Composable
fun ZweepTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = darkColorScheme(
            primary = Zw.accent,
            onPrimary = Color.White,
            secondary = Zw.brand,
            onSecondary = Color.White,
            background = Zw.background,
            onBackground = Zw.textBody,
            surface = Zw.surface,
            onSurface = Zw.textBody,
            surfaceVariant = Zw.surface,
            onSurfaceVariant = Zw.textSecondary,
            outline = Zw.outline,
            error = Zw.error,
        ),
        content = content,
    )
}
