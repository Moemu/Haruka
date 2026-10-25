package moe.snowy.haruka.ui

import android.Manifest
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.res.stringResource
import moe.snowy.haruka.R
import moe.snowy.haruka.data.LOCATION_LOCAL

/**
 * 定位三选的交互封装（设置页与仪表盘共用）：选「设备反解」先走运行时权限，
 * 拒绝时不静默回退，弹显式二选一——改用服务端解析或关闭（spec §6.3 B）。
 * 返回可直接接 SegmentedButton.onClick 的回调。
 */
@Composable
fun rememberLocationModeSelector(vm: AppViewModel): (String) -> Unit {
    var showDeniedDialog by remember { mutableStateOf(false) }
    val permissionLauncher = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) vm.setLocationMode(LOCATION_LOCAL) else showDeniedDialog = true
    }
    if (showDeniedDialog) {
        AlertDialog(
            onDismissRequest = { showDeniedDialog = false },
            title = { Text(stringResource(R.string.location_denied_title)) },
            text = { Text(stringResource(R.string.location_denied_text)) },
            confirmButton = {
                TextButton(onClick = { showDeniedDialog = false; vm.setLocationMode(moe.snowy.haruka.data.LOCATION_SERVER) }) {
                    Text(stringResource(R.string.location_denied_server))
                }
            },
            dismissButton = {
                TextButton(onClick = { showDeniedDialog = false; vm.setLocationMode(moe.snowy.haruka.data.LOCATION_OFF) }) {
                    Text(stringResource(R.string.location_denied_off))
                }
            },
        )
    }
    return { mode ->
        when (mode) {
            LOCATION_LOCAL -> {
                if (vm.hasLocationPermission()) vm.setLocationMode(mode)
                else permissionLauncher.launch(Manifest.permission.ACCESS_COARSE_LOCATION)
            }
            else -> vm.setLocationMode(mode)
        }
    }
}
