package moe.snowy.haruka.ui

import android.Manifest
import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
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
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Warning
import moe.snowy.haruka.R
import moe.snowy.haruka.ui.AppViewModel

/** 首启向导：服务器 → 设备凭据（401/403 区分校验）→ owner（可选）→ 通知权限 → 保活。 */
@Composable
fun OnboardingScreen(vm: AppViewModel, onDone: () -> Unit) {
    val context = LocalContext.current
    var step by rememberSaveable { mutableStateOf(1) }
    var serverUrl by rememberSaveable { mutableStateOf("") }
    var deviceId by rememberSaveable { mutableStateOf("") }
    var deviceToken by rememberSaveable { mutableStateOf("") }
    var ownerToken by rememberSaveable { mutableStateOf("") }
    var checkMsg by remember { mutableStateOf("") }
    var checkOk by remember { mutableStateOf(false) }
    var checking by remember { mutableStateOf(false) }

    // 第 4 步权限链：先申请身体活动（步数采集需要，MIUI 计步 provider 校验运行时授权），
    // 回调里再走通知申请；拒绝都只降级对应功能，不阻塞向导。
    val notifLauncher = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        checkMsg = if (granted) "" else context.getString(R.string.onboarding_step_notification_hint)
        checkOk = granted
        step = 5
    }
    val activityRecognitionLauncher = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { _ ->
        if (Build.VERSION.SDK_INT >= 33) {
            notifLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
        } else {
            advance(5)
        }
    }

    // 校验消息只在当前步骤有效，换步即清，避免上一步的「服务器可达」串到下一步
    fun advance(to: Int) {
        step = to
        checkMsg = ""
        checkOk = false
        checking = false
    }

    // Scaffold 提供状态栏/手势条内边距（enableEdgeToEdge 下标题不再顶进状态栏）；
    // imePadding 让文字步骤在键盘弹出时可完整滚动到可见区域
    Scaffold { innerPadding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(innerPadding)
                .imePadding()
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 24.dp, vertical = 24.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            Text(stringResource(R.string.onboarding_title), style = MaterialTheme.typography.headlineSmall)
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                // 轨道色显式给 surfaceVariant：部分动态取色下默认轨道与背景几乎同色调
                LinearProgressIndicator(
                    progress = { step / 5f },
                    modifier = Modifier.weight(1f),
                    trackColor = MaterialTheme.colorScheme.surfaceVariant,
                )
                Text(
                    "$step / 5",
                    style = MaterialTheme.typography.labelLarge,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }

            when (step) {
                1 -> {
                    StepTitle(stringResource(R.string.onboarding_step_server))
                    OutlinedTextField(
                        value = serverUrl,
                        onValueChange = { serverUrl = it },
                        label = { Text(stringResource(R.string.onboarding_step_server)) },
                        placeholder = { Text(stringResource(R.string.onboarding_step_server_hint)) },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    CheckFeedback(checkMsg, checkOk)
                    OutlinedButton(
                        onClick = {
                            checking = true
                            vm.checkServer(serverUrl) { ok, msg -> checkOk = ok; checkMsg = msg; checking = false }
                        },
                        enabled = !checking,
                    ) {
                        if (checking) CircularProgressIndicator(modifier = Modifier.size(16.dp), strokeWidth = 2.dp)
                        Text(stringResource(R.string.action_check))
                    }
                    Button(onClick = { advance(2) }, enabled = checkOk && !checking) {
                        Text(stringResource(R.string.action_next))
                    }
                }
                2 -> {
                    StepTitle(stringResource(R.string.onboarding_step_credentials))
                    Text(stringResource(R.string.onboarding_step_credentials_hint), style = MaterialTheme.typography.bodySmall)
                    OutlinedTextField(
                        value = deviceId,
                        onValueChange = { deviceId = it },
                        label = { Text(stringResource(R.string.field_device_id)) },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    OutlinedTextField(
                        value = deviceToken,
                        onValueChange = { deviceToken = it },
                        label = { Text(stringResource(R.string.field_device_token)) },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    CheckFeedback(checkMsg, checkOk)
                    OutlinedButton(
                        onClick = {
                            checking = true
                            vm.verifyCredentials(serverUrl, deviceId, deviceToken) { ok, msg ->
                                checkOk = ok; checkMsg = msg; checking = false
                            }
                        },
                        enabled = !checking,
                    ) {
                        if (checking) CircularProgressIndicator(modifier = Modifier.size(16.dp), strokeWidth = 2.dp)
                        Text(stringResource(R.string.action_check))
                    }
                    Button(onClick = { advance(3) }, enabled = checkOk && !checking) {
                        Text(stringResource(R.string.action_next))
                    }
                }
                3 -> {
                    StepTitle(stringResource(R.string.onboarding_step_owner))
                    Text(stringResource(R.string.onboarding_step_owner_hint), style = MaterialTheme.typography.bodySmall)
                    OutlinedTextField(
                        value = ownerToken,
                        onValueChange = { ownerToken = it },
                        label = { Text(stringResource(R.string.field_owner_token)) },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        OutlinedButton(onClick = { ownerToken = ""; advance(4) }) { Text(stringResource(R.string.action_skip)) }
                        Button(onClick = { advance(4) }) { Text(stringResource(R.string.action_next)) }
                    }
                }
                4 -> {
                    StepTitle(stringResource(R.string.onboarding_step_notification))
                    Text(stringResource(R.string.onboarding_step_notification_hint), style = MaterialTheme.typography.bodySmall)
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        OutlinedButton(onClick = { advance(5) }) { Text(stringResource(R.string.action_skip)) }
                        Button(onClick = {
                            activityRecognitionLauncher.launch(Manifest.permission.ACTIVITY_RECOGNITION)
                        }) { Text(stringResource(R.string.action_next)) }
                    }
                }
                5 -> {
                    StepTitle(stringResource(R.string.onboarding_step_battery))
                    Text(stringResource(R.string.onboarding_step_battery_hint), style = MaterialTheme.typography.bodySmall)
                    if (vm.batteryWhitelisted()) {
                        CheckFeedback(context.getString(R.string.settings_battery_ok), ok = true)
                    } else {
                        OutlinedButton(onClick = {
                            try {
                                context.startActivity(
                                    Intent(
                                        Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                                        Uri.parse("package:${context.packageName}"),
                                    ),
                                )
                            } catch (e: ActivityNotFoundException) {
                                // 厂商裁剪了该入口；保活指引在设置页仍有说明
                            }
                        }) { Text(stringResource(R.string.settings_battery_whitelist)) }
                    }
                    Button(onClick = {
                        vm.completeOnboarding(serverUrl, deviceId, deviceToken, ownerToken)
                        onDone()
                    }) { Text(stringResource(R.string.action_done)) }
                }
            }

            if (step > 1) {
                TextButton(onClick = { advance(step - 1) }) {
                    Text(stringResource(R.string.action_back))
                }
            }
        }
    }
}

/** 校验反馈行：图标 + 文案，成功/失败配色随主题语义色，不依赖单一颜色传义。 */
@Composable
private fun CheckFeedback(msg: String, ok: Boolean) {
    if (msg.isEmpty()) return
    Row(
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        Icon(
            if (ok) Icons.Filled.CheckCircle else Icons.Filled.Warning,
            contentDescription = null,
            tint = if (ok) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.error,
            modifier = Modifier.size(18.dp),
        )
        Text(
            msg,
            style = MaterialTheme.typography.bodyMedium,
            color = if (ok) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.error,
        )
    }
}

@Composable
private fun StepTitle(text: String) {
    Text(text, style = MaterialTheme.typography.titleLarge)
}
