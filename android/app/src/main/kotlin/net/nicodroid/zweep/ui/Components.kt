// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.ui

import android.provider.Settings
import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.foundation.clickable
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.TextButton
import androidx.compose.material3.Button
import androidx.compose.material3.Surface
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.window.Dialog
import androidx.compose.material3.OutlinedTextField
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.luminance
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.PlatformTextStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.LineHeightStyle
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import net.nicodroid.zweep.R
import net.nicodroid.zweep.service.Notifier
import java.text.DateFormat
import java.util.Date

/** Severity never by color alone: bar plus label (accessibility rule) */
@Composable
fun SeverityBar(sev: Int, modifier: Modifier = Modifier, color: Color? = null) {
    Box(modifier.width(4.dp).fillMaxHeight().background(color ?: Zw.severity(sev), RoundedCornerShape(2.dp)))
}

@Composable
fun SeverityBadge(sev: Int) {
    Text(
        stringResource(Notifier.severityLabel(sev)).uppercase(),
        color = Zw.onSeverity,
        fontSize = 11.sp,
        fontWeight = FontWeight.Bold,
        modifier = Modifier.background(Zw.severity(sev), RoundedCornerShape(4.dp)).padding(horizontal = 6.dp, vertical = 2.dp),
    )
}

/** Badge of a custom channel (name and color); the severity badge follows when the event has one */
@Composable
fun ChannelBadge(name: String, hex: String) {
    val fill = channelColor(hex) ?: Zw.outlineStrong
    Text(
        name.uppercase(),
        color = if (fill.luminance() < 0.3f) Color.White else Zw.onSeverity,
        fontSize = 11.sp,
        fontWeight = FontWeight.Bold,
        maxLines = 1,
        overflow = TextOverflow.Ellipsis,
        modifier = Modifier.background(fill, RoundedCornerShape(4.dp)).padding(horizontal = 6.dp, vertical = 2.dp),
    )
}

@Composable
fun StatusLabel(text: String, color: Color) {
    Text(text.uppercase(), color = color, fontSize = 11.sp, fontWeight = FontWeight.Bold)
}

/** Blinking unread dot; steady when the user disabled animations */
@Composable
fun UnreadDot() {
    val ctx = LocalContext.current
    val motion = remember { Settings.Global.getFloat(ctx.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) > 0f }
    val alpha = if (!motion) 1f else rememberInfiniteTransition(label = "unread").animateFloat(
        initialValue = 1f, targetValue = 0.2f,
        animationSpec = infiniteRepeatable(tween(800, easing = FastOutSlowInEasing), RepeatMode.Reverse), label = "unread-alpha",
    ).value
    Box(Modifier.size(8.dp).graphicsLayer { this.alpha = alpha }.background(Zw.accent, CircleShape))
}

@Composable
fun Header(title: String, onBack: (() -> Unit)? = null, titleSize: Int = 20, actions: @Composable () -> Unit = {}) {
    // Fixed height, everything centered on the same line; the title has no font padding, so its
    // glyphs and the icons share the same vertical center
    Row(
        Modifier.fillMaxWidth().height(56.dp).background(Zw.surface).padding(horizontal = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        if (onBack != null) {
            IconButton(onClick = onBack) { Icon(painterResource(R.drawable.ic_arrow_forward), stringResource(R.string.action_back), Modifier.rotate(180f), tint = Zw.textPrimary) }
        }
        Text(
            title, color = Zw.textPrimary, fontSize = titleSize.sp, fontWeight = FontWeight.Bold,
            style = TextStyle(
                platformStyle = PlatformTextStyle(includeFontPadding = false),
                lineHeightStyle = LineHeightStyle(LineHeightStyle.Alignment.Center, LineHeightStyle.Trim.Both),
            ),
            modifier = Modifier.weight(1f).padding(start = if (onBack == null) 8.dp else 0.dp),
        )
        actions()
    }
}

/** Thin line between the sections of a settings page */
@Composable
fun SectionDivider() {
    Box(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 14.dp).height(1.dp).background(Zw.outline.copy(alpha = 0.5f)))
}

@Composable
fun SectionTitle(text: String, inset: Boolean = true) {
    // inset: the title adds the side margin itself (lists); false inside a column that already has it
    val side = if (inset) 16.dp else 0.dp
    Text(text.uppercase(), color = Zw.textSecondary, fontSize = 12.sp, fontWeight = FontWeight.Bold, letterSpacing = 0.8.sp,
        modifier = Modifier.padding(start = side, end = side, top = 20.dp, bottom = 6.dp))
}

fun timeShort(ms: Long): String {
    val d = Date(ms)
    val today = DateFormat.getDateInstance(DateFormat.SHORT).format(Date())
    val day = DateFormat.getDateInstance(DateFormat.SHORT).format(d)
    val time = DateFormat.getTimeInstance(DateFormat.SHORT).format(d)
    return if (day == today) time else "$day $time"
}

fun duration(fromMs: Long, toMs: Long = System.currentTimeMillis()): String {
    val m = ((toMs - fromMs) / 60_000).coerceAtLeast(0)
    return when {
        m < 60 -> "${m}m"
        m < 24 * 60 -> "${m / 60}h ${m % 60}m"
        else -> "${m / (24 * 60)}d ${(m / 60) % 24}h"
    }
}

/** ISO-8601 from the server to epoch milliseconds */
fun isoMs(iso: String?): Long = net.nicodroid.zweep.core.isoMillis(iso) ?: 0

/** Short label of a period in minutes: 1h … 24h, then days (7d, 30d) */
@Composable
fun periodLabel(minutes: Int): String =
    if (minutes <= 1440) stringResource(R.string.duration_hours_short, minutes / 60) else stringResource(R.string.duration_days_short, minutes / 1440)

/** Severity filter: one checkbox per severity; the menu stays open while ticking */
@Composable
fun SeverityFilter(selected: Set<Int>, onChange: (Set<Int>) -> Unit) {
    var open by remember { mutableStateOf(false) }
    Column {
        FilterChip(
            selected.size < 6, { open = true },
            label = { Text(if (selected.size == 6) stringResource(R.string.filter_severity) else stringResource(R.string.filter_severity_count, selected.size)) },
        )
        DropdownMenu(open, { open = false }) {
            (5 downTo 0).forEach { s ->
                DropdownMenuItem(
                    text = { Text(stringResource(Notifier.severityLabel(s))) },
                    leadingIcon = { Checkbox(s in selected, null) },
                    onClick = { onChange(if (s in selected) selected - s else selected + s) },
                )
            }
            DropdownMenuItem(
                text = { Text(stringResource(R.string.filter_select_all), modifier = Modifier.fillMaxWidth(), textAlign = TextAlign.Center) },
                onClick = { onChange((0..5).toSet()) },
            )
        }
    }
}

/** Single-choice filter (e.g. status): the chip shows the current choice */
@Composable
fun ChoiceFilter(options: List<Pair<String, String>>, selected: String, onSelect: (String) -> Unit) {
    var open by remember { mutableStateOf(false) }
    Column {
        FilterChip(selected != options.first().first, { open = true }, label = { Text(options.firstOrNull { it.first == selected }?.second ?: options.first().second) })
        DropdownMenu(open, { open = false }) {
            options.forEach { (key, label) ->
                DropdownMenuItem(
                    text = { Text(label) },
                    leadingIcon = { RadioButton(key == selected, null) },
                    onClick = { onSelect(key); open = false },
                )
            }
        }
    }
}

/** Color of a custom channel (#RRGGBB from the dashboard palette), or null */
fun channelColor(hex: String): Color? =
    hex.takeIf { it.length == 7 && it[0] == '#' }?.substring(1)?.toLongOrNull(16)?.let { Color(0xFF000000 or it) }

/** Dot of a custom channel: its color, or an outline when the admin chose none */
@Composable
fun ChannelDot(hex: String, modifier: Modifier = Modifier) {
    val c = channelColor(hex)
    val m = modifier.size(10.dp)
    if (c != null) Box(m.background(c, CircleShape)) else Box(m.border(1.dp, Zw.textSecondary, CircleShape))
}

/** Name of the custom channel of an alarm, with its color */
@Composable
fun ChannelTag(name: String, hex: String, modifier: Modifier = Modifier) {
    Row(modifier, verticalAlignment = Alignment.CenterVertically) {
        ChannelDot(hex, Modifier.padding(end = 4.dp))
        Text(name, color = channelColor(hex) ?: Zw.textSecondary, fontSize = 12.sp, fontWeight = FontWeight.Medium, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/**
 * Host group filter: the chip opens a window that covers most of the screen, with a search box and
 * one checkbox per group; the choice is applied with Apply. A group includes its subgroups, as in
 * Zabbix ("Lab/Database" covers "Lab/Database/MySQL"). Empty selection: all.
 */
@Composable
fun HostGroupFilter(groups: List<String>, selected: Set<String>, onChange: (Set<String>) -> Unit) {
    var open by remember { mutableStateOf(false) }
    FilterChip(
        selected.isNotEmpty(), { open = true },
        label = { Text(if (selected.isEmpty()) stringResource(R.string.filter_hostgroup) else stringResource(R.string.filter_hostgroup_count, selected.size)) },
    )
    if (open) HostGroupDialog(groups, selected, onDismiss = { open = false }) { onChange(it); open = false }
}

@Composable
private fun HostGroupDialog(groups: List<String>, selected: Set<String>, onDismiss: () -> Unit, onApply: (Set<String>) -> Unit) {
    var draft by remember { mutableStateOf(selected) }
    var query by remember { mutableStateOf("") }
    // Groups chosen earlier but without problems now stay in the list, so they can be removed
    val rows = remember(groups, selected) { hostGroupTree((groups + selected).distinct()) }
    val shown = if (query.isBlank()) rows else rows.filter { it.path.contains(query, true) }.map { it.copy(depth = 0, label = it.path) }
    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Surface(Modifier.fillMaxWidth(0.94f).fillMaxHeight(0.88f), shape = RoundedCornerShape(12.dp), color = Zw.surface) {
            Column(Modifier.padding(16.dp)) {
                Text(stringResource(R.string.filter_hostgroup), color = Zw.textPrimary, fontSize = 20.sp, fontWeight = FontWeight.Bold)
                OutlinedTextField(query, { query = it }, Modifier.fillMaxWidth().padding(top = 12.dp),
                    placeholder = { Text(stringResource(R.string.filter_hostgroup_search)) }, singleLine = true)
                Text(
                    if (draft.isEmpty()) stringResource(R.string.filter_hostgroup_all) else stringResource(R.string.filter_hostgroup_selected, draft.size),
                    color = Zw.textSecondary, fontSize = 12.sp, modifier = Modifier.padding(top = 8.dp, bottom = 4.dp),
                )
                LazyColumn(Modifier.weight(1f).fillMaxWidth()) {
                    if (shown.isEmpty()) item { Text(stringResource(R.string.filter_hostgroup_none), color = Zw.textSecondary, modifier = Modifier.padding(12.dp)) }
                    items(shown, key = { it.path }) { r ->
                        val on = r.path in draft
                        Row(
                            Modifier.fillMaxWidth().clickable { draft = if (on) draft - r.path else draft + r.path }
                                .padding(start = (r.depth * 20).dp, top = 2.dp, bottom = 2.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Checkbox(on, null, Modifier.padding(end = 8.dp))
                            Text(r.label, color = Zw.textPrimary, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        }
                    }
                }
                Row(Modifier.fillMaxWidth().padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End)) {
                    TextButton({ onApply(emptySet()) }) { Text(stringResource(R.string.filter_select_all)) }
                    TextButton(onDismiss) { Text(stringResource(R.string.action_cancel)) }
                    Button({ onApply(draft) }, shape = Zw.control) { Text(stringResource(R.string.filter_apply)) }
                }
            }
        }
    }
}

/** One line of the host group list: indented under its nearest ancestor present in the list */
data class HostGroupRow(val path: String, val label: String, val depth: Int)

/**
 * Orders the groups as a tree. A group is indented only under an ancestor that is itself in the list,
 * and then shows only the rest of its name ("MySQL" under "Lab/Database"); a group whose ancestors are
 * absent is a root and shows its full path ("Lab/Network/Core").
 */
fun hostGroupTree(groups: List<String>): List<HostGroupRow> {
    val set = groups.toSet()
    fun parentOf(g: String): String? {
        var p = g
        while (p.contains('/')) {
            p = p.substringBeforeLast('/')
            if (p in set) return p
        }
        return null
    }
    val children = groups.groupBy { parentOf(it) }
    val out = mutableListOf<HostGroupRow>()
    fun walk(parent: String?, depth: Int) {
        children[parent].orEmpty().sortedBy { it.lowercase() }.forEach { g ->
            out += HostGroupRow(g, if (parent == null) g else g.removePrefix("$parent/"), depth)
            walk(g, depth + 1)
        }
    }
    walk(null, 0)
    return out
}

/** Whether one of the host groups of a problem is a selected group or one of its subgroups */
fun inHostGroups(problemGroups: List<String>, selected: Set<String>): Boolean =
    selected.isEmpty() || problemGroups.any { g -> selected.any { s -> g == s || g.startsWith("$s/") } }

/**
 * Pull to refresh: pulling the list down at its top opens a strip above it with the Zweep logo. The
 * logo follows the finger (it grows, and its alarms appear one by one until the list is pulled far
 * enough); released there, a pulse leaves its centre and draws the logo: the wave goes out leaving
 * the red bands of the logo behind it, each alarm lights up as the wave reaches it, then it all
 * fades. The pulse is repeated while [onRefresh] works and always ends whole. Then the strip closes.
 */
@Composable
fun PullToRefresh(refreshing: Boolean, onRefresh: () -> Unit, modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    val density = androidx.compose.ui.platform.LocalDensity.current
    val threshold = with(density) { 140.dp.toPx() } // finger travel that asks for the refresh
    var pulled by remember { androidx.compose.runtime.mutableFloatStateOf(0f) }
    val refresh by androidx.compose.runtime.rememberUpdatedState(onRefresh)
    // A refresh is over in a moment: the pulse goes on to the end of the one it is in, so that it is
    // seen and never cut short. The time left is waited after the refresh ended: the pulse always stops.
    var shown by remember { mutableStateOf(false) }
    var startedAt by remember { androidx.compose.runtime.mutableLongStateOf(0L) }
    androidx.compose.runtime.LaunchedEffect(refreshing) {
        if (refreshing) {
            startedAt = android.os.SystemClock.uptimeMillis()
            shown = true
        } else if (shown) {
            kotlinx.coroutines.delay(PULSE_MS - (android.os.SystemClock.uptimeMillis() - startedAt) % PULSE_MS)
            shown = false
        }
    }
    val pulsing = refreshing || shown
    val busy by androidx.compose.runtime.rememberUpdatedState(pulsing)
    val connection = remember {
        object : androidx.compose.ui.input.nestedscroll.NestedScrollConnection {
            // Pushed back up: the strip closes before the list scrolls
            override fun onPreScroll(available: androidx.compose.ui.geometry.Offset, source: androidx.compose.ui.input.nestedscroll.NestedScrollSource): androidx.compose.ui.geometry.Offset {
                if (pulled <= 0f || available.y >= 0f) return androidx.compose.ui.geometry.Offset.Zero
                val used = maxOf(available.y, -pulled)
                pulled += used
                return androidx.compose.ui.geometry.Offset(0f, used)
            }

            // Dragged down with the list already at its top: what the list did not use opens the strip
            override fun onPostScroll(consumed: androidx.compose.ui.geometry.Offset, available: androidx.compose.ui.geometry.Offset, source: androidx.compose.ui.input.nestedscroll.NestedScrollSource): androidx.compose.ui.geometry.Offset {
                if (busy || source != androidx.compose.ui.input.nestedscroll.NestedScrollSource.UserInput || available.y <= 0f) return androidx.compose.ui.geometry.Offset.Zero
                pulled += available.y
                return androidx.compose.ui.geometry.Offset(0f, available.y)
            }

            // Released
            override suspend fun onPreFling(available: androidx.compose.ui.unit.Velocity): androidx.compose.ui.unit.Velocity {
                if (pulled <= 0f) return androidx.compose.ui.unit.Velocity.Zero
                val go = pulled > threshold && !busy
                pulled = 0f
                if (go) refresh()
                return available // no fling of the list out of a pull
            }
        }
    }
    // The pulse counts the frames itself: it moves also on a phone whose animations are reduced or off
    var elapsed by remember { androidx.compose.runtime.mutableFloatStateOf(0f) } // ms since the pulse began
    androidx.compose.runtime.LaunchedEffect(pulsing) {
        if (!pulsing) {
            elapsed = 0f
            return@LaunchedEffect
        }
        val start = androidx.compose.runtime.withFrameNanos { it }
        while (true) {
            androidx.compose.runtime.withFrameNanos { now -> elapsed = (now - start) / 1_000_000f }
        }
    }
    val progress = (pulled / threshold).coerceIn(0f, 1f)
    val height = if (pulsing) 64.dp else minOf(with(density) { (pulled / 2.5f).toDp() }, 64.dp)
    Column(modifier.nestedScroll(connection)) {
        if (height > 0.dp) {
            Box(Modifier.fillMaxWidth().height(height).clipToBounds(), contentAlignment = Alignment.Center) {
                androidx.compose.foundation.Canvas(Modifier.size(52.dp)) { zweepLogo(if (pulsing) 1f else progress, if (pulsing) elapsed else -1f) }
            }
        }
        Box(Modifier.weight(1f)) { content() }
    }
}

/** One pulse of the logo, a second and a half: the wave goes out in [WAVE_MS], the logo stays whole, then fades from [FADE_FROM_MS] */
private const val PULSE_MS = 1500L
private const val WAVE_MS = 850f
private const val FADE_FROM_MS = 1050f

/** An alarm of the logo, in the units of its drawing: where it is from the centre, its size and opacity, and those of its halo */
private class LogoAlarm(val x: Float, val y: Float, val r: Float, val opacity: Float, val halo: Float, val haloOpacity: Float) {
    val far = kotlin.math.hypot(x, y)
}

private val LOGO_ALARMS = listOf(
    LogoAlarm(121.9f, -156.0f, 15f, 1.00f, 45.0f, 0.52f), LogoAlarm(-162.8f, -94.0f, 11.5f, 0.88f, 32.0f, 0.45f),
    LogoAlarm(-126.6f, 122.3f, 9f, 0.75f, 23.0f, 0.39f), LogoAlarm(152.8f, 74.5f, 7f, 0.67f, 16.8f, 0.34f),
    LogoAlarm(-23.1f, -164.4f, 5f, 0.59f, 11.3f, 0.30f), LogoAlarm(53.0f, 212.5f, 4f, 0.54f, 8.6f, 0.27f),
)

/** The red bands around the disc of the logo: from, to, opacity */
private val LOGO_BANDS = listOf(Triple(103.5f, 140f, 0.42f), Triple(140f, 168f, 0.31f), Triple(168f, 198f, 0.21f), Triple(198f, 227f, 0.11f))
private const val LOGO_DISC = 111.5f

/**
 * The Zweep logo as it is drawn, small: the red disc with its Z, the ring of the radar, the six
 * alarms at their own sizes and the red bands around the disc. [shown] 0..1: how much of it is there
 * while the list is pulled (it grows, the alarms appear one by one). [pulse]: milliseconds since the
 * pulse began, negative when still: a wave leaves the disc and the bands appear behind it, each alarm
 * lights up with its halo as the wave reaches it, the whole logo stays a moment, then the bands and
 * the halos fade. A halo never comes near the disc of the Z.
 */
private fun androidx.compose.ui.graphics.drawscope.DrawScope.zweepLogo(shown: Float, pulse: Float) {
    val c = center
    // The drawing of the logo is 464 units wide (a radius of 232 around its centre)
    val u = size.minDimension / 464f * (0.6f + 0.4f * shown)
    val red = Zw.brand
    val warm = androidx.compose.ui.graphics.Color(0xFFE5392F) // the red of the bands and of the alarms
    val white = androidx.compose.ui.graphics.Color.White
    // How bright each alarm is, and how much of its halo is there
    val lit: List<Float>
    if (pulse < 0f) {
        lit = LOGO_ALARMS.indices.map { i -> if (shown * LOGO_ALARMS.size > i) 1f else 0f }
    } else {
        val p = pulse % PULSE_MS
        val f = (p / WAVE_MS).coerceIn(0f, 1f).let { 1f - (1f - it) * (1f - it) } // quick at first, then slower
        val front = LOGO_BANDS.first().first + (LOGO_BANDS.last().second - LOGO_BANDS.first().first) * f
        val fade = 1f - ((p - FADE_FROM_MS) / (PULSE_MS - 50f - FADE_FROM_MS)).coerceIn(0f, 1f)
        // The trail: each band is there as far as the wave has gone
        for ((from, to, opacity) in LOGO_BANDS) {
            if (front <= from) continue
            val end = minOf(to, front)
            drawCircle(warm.copy(alpha = opacity * fade), radius = (from + end) / 2 * u, center = c,
                style = androidx.compose.ui.graphics.drawscope.Stroke((end - from) * u))
        }
        if (f < 1f) {
            drawCircle(warm.copy(alpha = 0.5f * (1f - 0.6f * f)), radius = front * u, center = c,
                style = androidx.compose.ui.graphics.drawscope.Stroke(maxOf(3f * u, 1.5f)))
        }
        lit = LOGO_ALARMS.map { a -> ((front - a.far + 12f) / 24f).coerceIn(0f, 1f) * fade }
    }
    drawCircle(white.copy(alpha = 0.35f * shown), radius = 160f * u, center = c, style = androidx.compose.ui.graphics.drawscope.Stroke(maxOf(2.5f * u, 1.5f)))
    LOGO_ALARMS.forEachIndexed { i, a ->
        val at = androidx.compose.ui.geometry.Offset(c.x + a.x * u, c.y + a.y * u)
        val on = lit[i]
        if (pulse >= 0f && on > 0f) {
            // The halo stops well before the disc of the Z
            val reach = minOf(a.halo * (1f + 0.25f * on), a.far - LOGO_DISC - 18f) * u
            drawCircle(
                androidx.compose.ui.graphics.Brush.radialGradient(
                    listOf(androidx.compose.ui.graphics.Color(0xFFFF6A3A).copy(alpha = a.haloOpacity * on * shown), red.copy(alpha = 0f)), center = at, radius = reach,
                ),
                radius = reach, center = at,
            )
        }
        drawCircle(warm.copy(alpha = a.opacity * (0.35f + 0.65f * on) * shown), radius = maxOf(a.r * u, 1.5f), center = at)
    }
    drawCircle(red.copy(alpha = 0.4f + 0.6f * shown), radius = LOGO_DISC * u, center = c)
    // The Z of the logo
    val z = androidx.compose.ui.graphics.Path().apply {
        val pts = listOf(-40f to -48f, 40f to -48f, 40f to -31f, -15.38f to 31f, 40f to 31f, 40f to 48f, -40f to 48f, -40f to 31f, 15.38f to -31f, -40f to -31f)
        pts.forEachIndexed { i, (x, y) -> if (i == 0) moveTo(c.x + x * u, c.y + y * u) else lineTo(c.x + x * u, c.y + y * u) }
        close()
    }
    drawPath(z, white.copy(alpha = 0.4f + 0.6f * shown))
}
