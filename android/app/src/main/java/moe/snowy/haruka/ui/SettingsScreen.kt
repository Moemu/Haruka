package moe.snowy.haruka.ui

import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Tab
import androidx.compose.material3.TabRow
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import moe.snowy.haruka.BuildConfig
import moe.snowy.haruka.R
import moe.snowy.haruka.data.LOCATION_LOCAL
import moe.snowy.haruka.data.LOCATION_OFF
import moe.snowy.haruka.data.LOCATION_SERVER

/**
 * 页面三：设置（spec §4.3，FlClash 式类型分页）：常规 / 控制 / 高级 / 关于。
 * 运行日志默认不展示（LogStore 文件存储），高级页提供诊断入口。
 */
@Composable
fun SettingsScreen(vm: AppViewModel, onOpenKeepAlive: () -> Unit, onOpenLogs: () -> Unit) {
    val tabs = listOf(
        stringResource(R.string.settings_tab_general),
        stringResource(R.string.settings_tab_control),
        stringResource(R.string.settings_tab_advanced),
        stringResource(R.string.settings_tab_about),
    )
    var tab by rememberSaveable { mutableIntStateOf(0) }

    Column(modifier = Modifier.fillMaxSize()) {
        TabRow(selectedTabIndex = tab) {
            tabs.forEachIndexed { i, label ->
                Tab(selected = tab == i, onClick = { tab = i }, text = { Text(label) })
            }
        }
        when (tab) {
            0 -> GeneralTab(vm)
            1 -> ControlTab(vm)
            2 -> AdvancedTab(vm, onOpenKeepAlive, onOpenLogs)
            else -> AboutTab()
        }
    }
}

@Composable
private fun GeneralTab(vm: AppViewModel) {
    val context = LocalContext.current
    val settings by vm.settings.collectAsStateWithLifecycle()
    val locSyncing by vm.locSyncing.collectAsStateWithLifecycle()
    var interval by remember(settings.heartbeatMin) { mutableStateOf(settings.heartbeatMin) }
    val selectLocationMode = rememberLocationModeSelector(vm)

    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        item {
            SettingsSection(stringResource(R.string.settings_section_server)) {
                SettingRow("服务器地址", settings.baseUrl.ifBlank { "—" })
                SettingRow(stringResource(R.string.field_device_id), settings.deviceId)
                SettingRow(stringResource(R.string.field_device_token), mask(settings.deviceToken))
                SettingRow(stringResource(R.string.field_owner_token), mask(settings.ownerToken))
            }
        }
        item {
            SettingsSection(stringResource(R.string.settings_section_collect)) {
                Text(stringResource(R.string.settings_heartbeat_interval), style = MaterialTheme.typography.bodyMedium)
                SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
                    listOf(5, 10, 15).forEachIndexed { i, min ->
                        SegmentedButton(
                            selected = interval == min,
                            onClick = { interval = min; vm.applyHeartbeat(min) },
                            shape = SegmentedButtonDefaults.itemShape(index = i, count = 3),
                        ) { Text("${min}m") }
                    }
                }
                Text(stringResource(R.string.settings_heartbeat_hint), style = MaterialTheme.typography.bodySmall)
                HorizontalDivider()
                Text(stringResource(R.string.collector_location), style = MaterialTheme.typography.bodyMedium)
                SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
                    val modes = listOf(
                        LOCATION_SERVER to stringResource(R.string.location_mode_server),
                        LOCATION_LOCAL to stringResource(R.string.location_mode_local),
                        LOCATION_OFF to stringResource(R.string.location_mode_off),
                    )
                    modes.forEachIndexed { i, (mode, label) ->
                        SegmentedButton(
                            // 只回显服务端确认后的模式；同步失败时自动弹回原值
                            selected = settings.locationMode == mode,
                            onClick = { selectLocationMode(mode) },
                            shape = SegmentedButtonDefaults.itemShape(index = i, count = modes.size),
                        ) { Text(label, style = MaterialTheme.typography.labelMedium) }
                    }
                }
                if (locSyncing) {
                    Text(
                        stringResource(R.string.location_syncing),
                        color = MaterialTheme.colorScheme.tertiary,
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
                Text(stringResource(R.string.location_hint), style = MaterialTheme.typography.bodySmall)
                if (settings.locationMode == LOCATION_LOCAL) {
                    var homeInput by remember(settings.homeDistrict) { mutableStateOf(settings.homeDistrict) }
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        OutlinedTextField(
                            value = homeInput,
                            onValueChange = { homeInput = it },
                            label = { Text(stringResource(R.string.settings_home_district)) },
                            singleLine = true,
                            modifier = Modifier.weight(1f),
                        )
                        TextButton(onClick = { vm.setHomeDistrict(homeInput) }) {
                            Text(stringResource(R.string.settings_home_district_save))
                        }
                    }
                    Text(stringResource(R.string.settings_home_district_hint), style = MaterialTheme.typography.bodySmall)
                }
            }
        }
    }
}

@Composable
private fun ControlTab(vm: AppViewModel) {
    val settings by vm.settings.collectAsStateWithLifecycle()

    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        item {
            // 控制分级（设计案 §6.4）：设备暂停与全局隐身两张卡视觉分开
            SettingsSection(stringResource(R.string.settings_pause_device)) {
                if (settings.pausedUntilMs > System.currentTimeMillis()) {
                    Text(
                        stringResource(R.string.status_paused) + "（至 ${shortDateTime(settings.pausedUntilMs)}）",
                        color = MaterialTheme.colorScheme.tertiary,
                        style = MaterialTheme.typography.bodyMedium,
                    )
                    OutlinedButton(onClick = { vm.resumeDevice() }) { Text(stringResource(R.string.settings_resume)) }
                } else {
                    Row2 {
                        listOf(1 to stringResource(R.string.settings_pause_1h), 2 to stringResource(R.string.settings_pause_2h), 8 to stringResource(R.string.settings_pause_8h)).forEach { (h, label) ->
                            OutlinedButton(onClick = { vm.pauseDevice(h) }) { Text(label) }
                        }
                    }
                }
            }
        }
        item {
            SettingsSection(stringResource(R.string.settings_hide)) {
                if (settings.pendingControl.isNotBlank()) {
                    Text(
                        "公开隐藏未确认（等待网络）",
                        color = MaterialTheme.colorScheme.tertiary,
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
                if (settings.ownerToken.isBlank()) {
                    Text(stringResource(R.string.settings_needs_owner), style = MaterialTheme.typography.bodySmall)
                }
                Row2 {
                    listOf(
                        1 to stringResource(R.string.settings_hide_1h),
                        4 to stringResource(R.string.settings_hide_4h),
                        -1 to stringResource(R.string.settings_hide_forever),
                    ).forEach { (h, label) ->
                        OutlinedButton(
                            onClick = { vm.control("hide", if (h < 0) null else System.currentTimeMillis() + h * 3_600_000L) },
                            enabled = settings.ownerToken.isNotBlank(),
                        ) { Text(label) }
                    }
                }
                Button(onClick = { vm.control("show", null) }, enabled = settings.ownerToken.isNotBlank()) {
                    Text(stringResource(R.string.settings_show))
                }
            }
        }
    }
}

@Composable
private fun AdvancedTab(vm: AppViewModel, onOpenKeepAlive: () -> Unit, onOpenLogs: () -> Unit) {
    val context = LocalContext.current

    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        item {
            SettingsSection(stringResource(R.string.settings_section_keepalive)) {
                val whitelisted = vm.batteryWhitelisted()
                Text(
                    if (whitelisted) stringResource(R.string.settings_battery_ok)
                    else stringResource(R.string.settings_battery_missing),
                    color = if (whitelisted) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.tertiary,
                    style = MaterialTheme.typography.bodyMedium,
                )
                if (!whitelisted) {
                    Button(onClick = {
                        try {
                            context.startActivity(
                                Intent(
                                    Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                                    Uri.parse("package:${context.packageName}"),
                                ),
                            )
                        } catch (_: ActivityNotFoundException) {
                            // 厂商裁剪了该入口
                        }
                    }) { Text(stringResource(R.string.settings_battery_whitelist)) }
                }
                OutlinedButton(onClick = onOpenKeepAlive) { Text(stringResource(R.string.settings_open_keepalive)) }
            }
        }
        item {
            SettingsSection(stringResource(R.string.settings_diag)) {
                Text(
                    stringResource(R.string.settings_diag_hint),
                    style = MaterialTheme.typography.bodySmall,
                )
                OutlinedButton(onClick = onOpenLogs) { Text(stringResource(R.string.settings_open_logs)) }
            }
        }
    }
}

@Composable
private fun AboutTab() {
    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        item {
            Card(modifier = Modifier.fillMaxWidth()) {
                Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(stringResource(R.string.about_app_line), style = MaterialTheme.typography.titleMedium)
                    Text(
                        stringResource(R.string.about_version, BuildConfig.VERSION_NAME),
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    Text(
                        stringResource(R.string.about_homepage),
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
        }
        item {
            SettingsSection(stringResource(R.string.about_privacy_title)) {
                Text(stringResource(R.string.about_privacy_text), style = MaterialTheme.typography.bodyMedium)
            }
        }
        item {
            SettingsSection(stringResource(R.string.about_boundaries_title)) {
                Text(stringResource(R.string.about_boundaries_text), style = MaterialTheme.typography.bodyMedium)
            }
        }
        item {
            SettingsSection(stringResource(R.string.about_license_title)) {
                Text(stringResource(R.string.about_license_text), style = MaterialTheme.typography.bodyMedium)
            }
        }
        item {
            SettingsSection(stringResource(R.string.about_docs_title)) {
                Text(stringResource(R.string.about_docs_text), style = MaterialTheme.typography.bodyMedium)
            }
        }
    }
}

private fun mask(token: String): String =
    if (token.isBlank()) "—" else "••••" + token.takeLast(4)

@Composable
private fun SettingsSection(title: String, content: @Composable androidx.compose.foundation.layout.ColumnScope.() -> Unit) {
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Text(title, style = MaterialTheme.typography.titleMedium)
            content()
        }
    }
}

@Composable
private fun SettingRow(label: String, value: String) {
    androidx.compose.foundation.layout.Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.SpaceBetween,
    ) {
        Text(label, style = MaterialTheme.typography.bodyMedium)
        Text(value, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

@Composable
private fun Row2(content: @Composable androidx.compose.foundation.layout.RowScope.() -> Unit) {
    androidx.compose.foundation.layout.Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        content = content,
    )
}
