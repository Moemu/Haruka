package moe.snowy.haruka.ui

import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Info
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import moe.snowy.haruka.R

/**
 * 页面四：保活引导（spec §4.4）——澎湃 OS 专项，一屏三步 + 折叠说明。
 * 只做检测与指引，**不做任何自动改设置的尝试**；无法自动确认的步骤以 ？ 呈现。
 */
@Composable
fun KeepAliveScreen(vm: AppViewModel, onBack: () -> Unit) {
    val context = LocalContext.current
    val whitelisted = vm.batteryWhitelisted()
    var expanded by remember { mutableStateOf(false) }

    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        item {
            Row(verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = onBack) {
                    Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = stringResource(R.string.action_back))
                }
                Text(stringResource(R.string.keepalive_title), style = MaterialTheme.typography.titleLarge)
            }
        }
        item {
            KeepAliveStep(
                done = whitelisted,
                title = stringResource(R.string.keepalive_step_battery),
                path = stringResource(R.string.keepalive_step_battery_path),
            ) {
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
                    }) { Text(stringResource(R.string.keepalive_step_battery_apply)) }
                }
            }
        }
        item {
            KeepAliveStep(
                done = false,
                title = stringResource(R.string.keepalive_step_powersave),
                path = stringResource(R.string.keepalive_step_powersave_path),
            )
        }
        item {
            KeepAliveStep(
                done = false,
                title = stringResource(R.string.keepalive_step_autostart),
                path = stringResource(R.string.keepalive_step_autostart_path),
            )
        }
        item {
            Card(modifier = Modifier.fillMaxWidth()) {
                Column(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clickable { expanded = !expanded }
                        .padding(16.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    Text(
                        stringResource(R.string.keepalive_more_title) + if (expanded) " ▾" else " ▸",
                        style = MaterialTheme.typography.titleSmall,
                    )
                    if (expanded) {
                        Text(stringResource(R.string.keepalive_more_body), style = MaterialTheme.typography.bodyMedium)
                    }
                }
            }
        }
        item {
            Text(stringResource(R.string.keepalive_promise), style = MaterialTheme.typography.bodySmall)
        }
    }
}

@Composable
private fun KeepAliveStep(done: Boolean, title: String, path: String, extra: @Composable (() -> Unit)? = null) {
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Icon(
                    if (done) Icons.Filled.CheckCircle else Icons.Filled.Info,
                    contentDescription = null,
                    tint = if (done) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Text(title, style = MaterialTheme.typography.titleSmall)
            }
            Text(path, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            extra?.invoke()
        }
    }
}
