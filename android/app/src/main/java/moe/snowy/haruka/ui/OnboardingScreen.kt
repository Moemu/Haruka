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
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
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
    var notifGranted by rememberSaveable { mutableStateOf(false) }

    val notifLauncher = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        notifGranted = granted
        checkMsg = if (granted) "" else context.getString(R.string.onboarding_step_notification_hint)
        step = 5
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text(stringResource(R.string.onboarding_title), style = MaterialTheme.typography.headlineSmall)
        LinearProgressIndicator(progress = { step / 5f }, modifier = Modifier.fillMaxWidth())
        Text("$step / 5", style = MaterialTheme.typography.labelMedium)

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
                if (checkMsg.isNotEmpty()) {
                    Text(checkMsg, color = if (checkOk) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.error)
                }
                Button(onClick = { vm.checkServer(serverUrl) { ok, msg -> checkOk = ok; checkMsg = msg } }) {
                    Text(stringResource(R.string.action_check))
                }
                Button(
                    onClick = { step = 2 },
                    enabled = checkOk,
                ) { Text(stringResource(R.string.action_next)) }
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
                if (checkMsg.isNotEmpty()) {
                    Text(checkMsg, color = if (checkOk) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.error)
                }
                Button(onClick = {
                    vm.verifyCredentials(serverUrl, deviceId, deviceToken) { ok, msg -> checkOk = ok; checkMsg = msg }
                }) { Text(stringResource(R.string.action_check)) }
                Button(onClick = { step = 3 }, enabled = checkOk) { Text(stringResource(R.string.action_next)) }
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
                    OutlinedButton(onClick = { ownerToken = ""; step = 4 }) { Text(stringResource(R.string.action_skip)) }
                    Button(onClick = { step = 4 }) { Text(stringResource(R.string.action_next)) }
                }
            }
            4 -> {
                StepTitle(stringResource(R.string.onboarding_step_notification))
                Text(stringResource(R.string.onboarding_step_notification_hint), style = MaterialTheme.typography.bodySmall)
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedButton(onClick = { step = 5 }) { Text(stringResource(R.string.action_skip)) }
                    if (Build.VERSION.SDK_INT >= 33) {
                        Button(onClick = {
                            notifLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
                        }) { Text(stringResource(R.string.action_next)) }
                    } else {
                        Button(onClick = { step = 5 }) { Text(stringResource(R.string.action_next)) }
                    }
                }
            }
            5 -> {
                StepTitle(stringResource(R.string.onboarding_step_battery))
                Text(stringResource(R.string.onboarding_step_battery_hint), style = MaterialTheme.typography.bodySmall)
                if (vm.batteryWhitelisted()) {
                    Text(stringResource(R.string.settings_battery_ok), color = MaterialTheme.colorScheme.primary)
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
            TextButton(onClick = { step -= 1; checkMsg = ""; checkOk = false }) {
                Text(stringResource(R.string.action_back))
            }
        }
    }
}

@Composable
private fun StepTitle(text: String) {
    Text(text, style = MaterialTheme.typography.titleLarge)
}
