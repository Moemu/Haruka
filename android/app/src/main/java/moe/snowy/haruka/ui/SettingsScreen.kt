package moe.snowy.haruka.ui

import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
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
import moe.snowy.haruka.sync.ForegroundApp

/**
 * 页面三：设置（FlClash 式二级菜单）：入口列表进入各子页（服务器 / 采集 / 控制 / 关于），
 * 保活引导与运行日志直达既有页面。子页走 NavHost 子路由，返回手势与底部导航状态自然衔接。
 */

@Composable
fun SettingsScreen(
    vm: AppViewModel,
    onOpenServer: () -> Unit,
    onOpenCollect: () -> Unit,
    onOpenControl: () -> Unit,
    onOpenAbout: () -> Unit,
    onOpenKeepAlive: () -> Unit,
    onOpenLogs: () -> Unit,
) {
    Column(modifier = Modifier.fillMaxSize()) {
        Text(
            stringResource(R.string.nav_settings),
            style = MaterialTheme.typography.titleLarge,
            modifier = Modifier.padding(horizontal = 16.dp, vertical = 12.dp),
        )
        LazyColumn(contentPadding = PaddingValues(vertical = 8.dp)) {
            item { MenuEntry(stringResource(R.string.settings_section_server), onOpenServer) }
            item { MenuEntry(stringResource(R.string.settings_section_collect), onOpenCollect) }
            item { MenuEntry(stringResource(R.string.settings_section_control), onOpenControl) }
            item { MenuEntry(stringResource(R.string.keepalive_title), onOpenKeepAlive) }
            item { MenuEntry(stringResource(R.string.logs_title), onOpenLogs) }
            item { MenuEntry(stringResource(R.string.settings_tab_about), onOpenAbout) }
        }
    }
}

@Composable
private fun MenuEntry(title: String, onClick: () -> Unit) {
    ListItem(
        headlineContent = { Text(title) },
        trailingContent = {
            Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, contentDescription = null)
        },
        modifier = Modifier.clickable(onClick = onClick),
    )
}

/** 二级页骨架：返回行 + 可滚动内容（imePadding 兜键盘）。 */
@Composable
private fun SettingsSubPage(title: String, onBack: () -> Unit, content: @Composable ColumnScope.() -> Unit) {
    Column(modifier = Modifier.fillMaxSize()) {
        Row(
            modifier = Modifier.padding(horizontal = 8.dp, vertical = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            IconButton(onClick = onBack) {
                Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = stringResource(R.string.action_back))
            }
            Text(title, style = MaterialTheme.typography.titleLarge)
        }
        Column(
            modifier = Modifier
                .fillMaxSize()
                .imePadding()
                .verticalScroll(rememberScrollState())
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
            content = content,
        )
    }
}

@Composable
fun SettingsServerPage(vm: AppViewModel, onBack: () -> Unit) {
    val settings by vm.settings.collectAsStateWithLifecycle()
    SettingsSubPage(stringResource(R.string.settings_section_server), onBack) {
        SettingsSection(stringResource(R.string.settings_section_server)) {
            SettingRow("服务器地址", settings.baseUrl.ifBlank { "—" })
            SettingRow(stringResource(R.string.field_device_id), settings.deviceId)
            SettingRow(stringResource(R.string.field_device_token), mask(settings.deviceToken))
            SettingRow(stringResource(R.string.field_owner_token), mask(settings.ownerToken))
        }
    }
}

@Composable
fun SettingsCollectPage(vm: AppViewModel, onBack: () -> Unit) {
    val settings by vm.settings.collectAsStateWithLifecycle()
    val locSyncing by vm.locSyncing.collectAsStateWithLifecycle()
    var interval by remember(settings.heartbeatMin) { mutableStateOf(settings.heartbeatMin) }
    val selectLocationMode = rememberLocationModeSelector(vm)

    SettingsSubPage(stringResource(R.string.settings_section_collect), onBack) {
        SettingsSection(stringResource(R.string.settings_heartbeat_interval)) {
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
        }
        SettingsSection(stringResource(R.string.collector_activity)) {
            val ctx = LocalContext.current
            val granted = remember { ForegroundApp.usageAccessGranted(ctx) }
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(stringResource(R.string.activity_toggle), style = MaterialTheme.typography.bodyMedium)
                Switch(
                    checked = settings.activityEnabled,
                    onCheckedChange = { vm.setActivityEnabled(it) },
                )
            }
            Text(stringResource(R.string.activity_hint), style = MaterialTheme.typography.bodySmall)
            if (settings.activityEnabled && !granted) {
                Button(onClick = { ForegroundApp.usageAccessSettings(ctx) }) {
                    Text(stringResource(R.string.activity_grant))
                }
            }
        }
        SettingsSection(stringResource(R.string.collector_location)) {
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
                HorizontalDivider()
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
                        // 回显已保存值：保存结果（成功/未找到区县）经全局 Snackbar 反馈
                        supportingText = {
                            Text(
                                if (settings.homeDistrict.isBlank()) stringResource(R.string.settings_home_district_not_set)
                                else stringResource(R.string.settings_home_district_current, settings.homeDistrict),
                            )
                        },
                        singleLine = true,
                        modifier = Modifier.weight(1f),
                    )
                    TextButton(onClick = { vm.setHomeDistrict(homeInput) }) {
                        Text(stringResource(R.string.settings_home_district_save))
                    }
                }
                Text(stringResource(R.string.settings_home_district_hint), style = MaterialTheme.typography.bodySmall)
                var schoolInput by remember(settings.schoolDistrict) { mutableStateOf(settings.schoolDistrict) }
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    OutlinedTextField(
                        value = schoolInput,
                        onValueChange = { schoolInput = it },
                        label = { Text(stringResource(R.string.settings_school_district)) },
                        supportingText = {
                            Text(
                                if (settings.schoolDistrict.isBlank()) stringResource(R.string.settings_school_district_not_set)
                                else stringResource(R.string.settings_school_district_current, settings.schoolDistrict),
                            )
                        },
                        singleLine = true,
                        modifier = Modifier.weight(1f),
                    )
                    TextButton(onClick = { vm.setSchoolDistrict(schoolInput) }) {
                        Text(stringResource(R.string.settings_school_district_save))
                    }
                }
                Text(stringResource(R.string.settings_school_district_hint), style = MaterialTheme.typography.bodySmall)
            }
        }
    }
}

@Composable
fun SettingsControlPage(vm: AppViewModel, onBack: () -> Unit) {
    val settings by vm.settings.collectAsStateWithLifecycle()

    SettingsSubPage(stringResource(R.string.settings_section_control), onBack) {
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

@Composable
fun SettingsAboutPage(onBack: () -> Unit) {
    SettingsSubPage(stringResource(R.string.settings_tab_about), onBack) {
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
        SettingsSection(stringResource(R.string.about_privacy_title)) {
            Text(stringResource(R.string.about_privacy_text), style = MaterialTheme.typography.bodyMedium)
        }
        SettingsSection(stringResource(R.string.about_boundaries_title)) {
            Text(stringResource(R.string.about_boundaries_text), style = MaterialTheme.typography.bodyMedium)
        }
        SettingsSection(stringResource(R.string.about_license_title)) {
            Text(stringResource(R.string.about_license_text), style = MaterialTheme.typography.bodyMedium)
        }
        SettingsSection(stringResource(R.string.about_docs_title)) {
            Text(stringResource(R.string.about_docs_text), style = MaterialTheme.typography.bodyMedium)
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
