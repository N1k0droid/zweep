// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import net.nicodroid.zweep.R
import net.nicodroid.zweep.service.ConnState

/** Second bar under the title: overall server status; tap for the per-server detail */
@Composable
fun StatusStrip(vm: AppViewModel) {
    val servers by vm.servers.collectAsStateWithLifecycle()
    val links by vm.links.collectAsStateWithLifecycle()
    var detail by remember { mutableStateOf(false) }
    val total = servers.count { it.enabled }
    val ok = servers.count { it.enabled && links[it.id]?.state == ConnState.CONNECTED }
    val color = when {
        total > 0 && ok == total -> Zw.success
        ok > 0 -> Zw.warning
        else -> Zw.error
    }
    Row(
        Modifier.fillMaxWidth()
            .background(Color(0xFF1E1B4B)) // one color: the middle of the former gradient
            .clickable { detail = true }
            .padding(horizontal = 16.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(stringResource(R.string.status_bar_label), color = Zw.textSecondary, fontSize = 13.sp)
        Spacer(Modifier.size(10.dp).background(color, CircleShape))
        Text(stringResource(R.string.status_bar_count, ok, total), color = Zw.textBody, fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
    }
    if (detail) {
        AlertDialog(
            onDismissRequest = { detail = false },
            title = { Text(stringResource(R.string.status_detail_title)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    servers.forEach { s ->
                        val st = links[s.id]
                        val (label, c) = when (st?.state) {
                            ConnState.CONNECTED -> stringResource(R.string.conn_connected) to Zw.success
                            ConnState.CONNECTING -> stringResource(R.string.conn_connecting) to Zw.warningText
                            ConnState.WAITING -> stringResource(R.string.conn_reconnecting) to Zw.warningText
                            ConnState.REJECTED -> (if (st.detail == "revoked" || st.detail == "unauthorized") stringResource(R.string.conn_revoked) else stringResource(R.string.conn_rejected, st.detail ?: "")) to Zw.errorText
                            else -> stringResource(R.string.conn_disconnected) to Zw.textSecondary
                        }
                        Column {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Spacer(Modifier.size(8.dp).background(c, CircleShape))
                                Text(s.label, color = Zw.textPrimary, fontWeight = FontWeight.Bold)
                            }
                            Text(s.baseUrl, color = Zw.textSecondary, fontSize = 12.sp)
                            Text(label, color = c, fontSize = 13.sp)
                            Text(
                                stringResource(
                                    when {
                                        s.baseUrl.startsWith("http://") -> R.string.security_cleartext
                                        s.pins.isNotEmpty() -> R.string.security_pinned
                                        else -> R.string.security_tls
                                    },
                                ),
                                color = Zw.textSecondary, fontSize = 12.sp,
                            )
                            if (st?.state == ConnState.WAITING && !st.detail.isNullOrBlank()) {
                                Text(stringResource(R.string.status_last_error, st.detail), color = Zw.textSecondary, fontSize = 12.sp)
                            }
                        }
                    }
                }
            },
            confirmButton = { TextButton({ detail = false }) { Text(stringResource(R.string.action_close)) } },
        )
    }
}
