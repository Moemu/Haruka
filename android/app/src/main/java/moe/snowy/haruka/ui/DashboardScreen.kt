package moe.snowy.haruka.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ElevatedCard
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.delay
import moe.snowy.haruka.R
import moe.snowy.haruka.data.LOCATION_LOCAL
import moe.snowy.haruka.data.LOCATION_OFF
import moe.snowy.haruka.data.LOCATION_SERVER
import moe.snowy.haruka.net.PublicSnapshot
import moe.snowy.haruka.sync.RuntimeState
import moe.snowy.haruka.sync.isSyncStale

/** 页面一：状态仪表盘（spec §4.1）：服务状态卡 / 提示卡 / 快照预览卡 / 采集开关；日志走 LogStore。 */
@Composable
fun DashboardScreen(vm: AppViewModel, onOpenKeepAlive: () -> Unit) {
    val runtime by vm.runtime.collectAsStateWithLifecycle()
    val settings by vm.settings.collectAsStateWithLifecycle()
    val snapshot by vm.snapshot.collectAsStateWithLifecycle()
    var nowMs by remember { mutableLongStateOf(System.currentTimeMillis()) }

    LaunchedEffect(Unit) {
        vm.refreshSnapshot()
        while (true) {
            delay(30_000)
            nowMs = System.currentTimeMillis()
            vm.refreshSnapshot()
        }
    }

    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        item { ServiceStatusCard(runtime, settings.pausedUntilMs) }
        // 观测回路（spec §5.4）：服务确实在跑但明显超期未同步才提示，避免把主动停止误报成被系统限制
        if (runtime.running && isSyncStale(nowMs, settings.lastSyncMs, settings.heartbeatMin)) {
            item { StaleSyncCard((nowMs - settings.lastSyncMs) / 60_000, onOpenKeepAlive) }
        }
        item { SnapshotPreviewCard(snapshot) }
        item { CollectorsCard(vm, settings.locationMode) }
    }
}

@Composable
private fun StaleSyncCard(staleMinutes: Long, onOpenKeepAlive: () -> Unit) {
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Icon(
                    Icons.Filled.Warning,
                    contentDescription = null,
                    tint = MaterialTheme.colorScheme.tertiary,
                )
                Text(stringResource(R.string.stale_sync_title, staleMinutes), style = MaterialTheme.typography.titleSmall)
            }
            Text(stringResource(R.string.stale_sync_text), style = MaterialTheme.typography.bodySmall)
            TextButton(onClick = onOpenKeepAlive) { Text(stringResource(R.string.stale_sync_action)) }
        }
    }
}

@Composable
private fun ServiceStatusCard(runtime: RuntimeState, pausedUntilMs: Long) {
    ElevatedCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                StatusDot(runtime)
                Text(statusText(runtime, pausedUntilMs), style = MaterialTheme.typography.titleMedium)
            }
            if (runtime.running) {
                Text(
                    stringResource(R.string.status_last_sync, relativeTime(runtime.lastSyncMs)),
                    style = MaterialTheme.typography.bodySmall,
                )
                if (runtime.phase == RuntimeState.PHASE_OK && runtime.nextTickMs > 0) {
                    Text(
                        stringResource(R.string.status_next_tick, shortTime(runtime.nextTickMs)),
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
            }
        }
    }
}

@Composable
private fun StatusDot(runtime: RuntimeState) {
    // 状态点附文字语义，不靠颜色单独传义（spec §3.6 无障碍）
    val (color, desc) = when (runtime.phase) {
        RuntimeState.PHASE_OK -> MaterialTheme.colorScheme.primary to "运行中"
        RuntimeState.PHASE_WAITING, RuntimeState.PHASE_PAUSED -> MaterialTheme.colorScheme.tertiary to "等待中"
        RuntimeState.PHASE_AUTH -> MaterialTheme.colorScheme.error to "错误"
        else -> MaterialTheme.colorScheme.outline to "已停止"
    }
    Box(
        modifier = Modifier
            .size(12.dp)
            .background(color, CircleShape)
            .semantics { contentDescription = desc },
    )
}

@Composable
private fun statusText(runtime: RuntimeState, pausedUntilMs: Long): String = when {
    !runtime.running -> stringResource(R.string.status_stopped)
    runtime.phase == RuntimeState.PHASE_PAUSED ->
        stringResource(R.string.status_paused) + if (pausedUntilMs > 0) "（至 ${shortTime(pausedUntilMs)}）" else ""
    runtime.phase == RuntimeState.PHASE_AUTH -> stringResource(R.string.status_auth_error)
    runtime.phase == RuntimeState.PHASE_WAITING -> stringResource(R.string.status_waiting, runtime.queue)
    else -> stringResource(R.string.status_running)
}

@Composable
private fun SnapshotPreviewCard(snapshot: PublicSnapshot?) {
    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.secondaryContainer),
    ) {
        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Text(stringResource(R.string.snapshot_title), style = MaterialTheme.typography.titleMedium)
            when {
                snapshot == null -> Text(stringResource(R.string.snapshot_unavailable), style = MaterialTheme.typography.bodyMedium)
                snapshot.visibility == "hidden" -> Text(stringResource(R.string.snapshot_hidden), style = MaterialTheme.typography.bodyLarge)
                else -> {
                    val stateText = when (snapshot.presence?.state) {
                        "active" -> stringResource(R.string.snapshot_active)
                        "away" -> stringResource(R.string.snapshot_away)
                        "offline" -> stringResource(R.string.snapshot_offline)
                        null -> if (snapshot.online) stringResource(R.string.snapshot_unknown) else stringResource(R.string.snapshot_offline)
                        else -> stringResource(R.string.snapshot_unknown)
                    }
                    Text(stateText, style = MaterialTheme.typography.bodyLarge)
                    snapshot.location?.display?.takeIf { it.isNotBlank() }?.let {
                        Text("📍 $it", style = MaterialTheme.typography.bodyMedium)
                    }
                    snapshot.activity?.display?.takeIf { it.isNotBlank() }?.let {
                        Text(it, style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                    snapshot.mood?.let { m ->
                        val label = moodLabel(m.mood.orEmpty())
                        val parts = listOfNotNull(label, m.text?.takeIf { t -> t.isNotBlank() })
                        if (parts.isNotEmpty()) {
                            Text(parts.joinToString(" · "), style = MaterialTheme.typography.bodyMedium)
                        }
                    }
                }
            }
        }
    }
}

/** mood 枚举到展示映射，与状态页一致（design.md §10）。 */
fun moodLabel(mood: String): String? = when (mood) {
    "happy" -> "😊 开心"
    "focused" -> "🎯 专注"
    "tired" -> "😮‍💨 疲惫"
    "anxious" -> "😰 焦虑"
    "chilling" -> "🛋️ 摸鱼"
    "sick" -> "🤒 不适"
    else -> null
}

@Composable
private fun CollectorsCard(vm: AppViewModel, locationMode: String) {
    val selectLocationMode = rememberLocationModeSelector(vm)
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Text(stringResource(R.string.collector_location), style = MaterialTheme.typography.titleSmall)
            SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
                val modes = listOf(
                    LOCATION_SERVER to stringResource(R.string.location_mode_server),
                    LOCATION_LOCAL to stringResource(R.string.location_mode_local),
                    LOCATION_OFF to stringResource(R.string.location_mode_off),
                )
                modes.forEachIndexed { i, (mode, label) ->
                    SegmentedButton(
                        selected = locationMode == mode,
                        onClick = { if (locationMode != mode) selectLocationMode(mode) },
                        shape = SegmentedButtonDefaults.itemShape(index = i, count = modes.size),
                    ) { Text(label, style = MaterialTheme.typography.labelMedium) }
                }
            }
        }
    }
}
