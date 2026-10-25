package moe.snowy.haruka.sync

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import moe.snowy.haruka.HarukaApp

/** 重启后拉起心跳服务（属于允许的后台启动场景）；被拒时记日志并靠 WorkManager 兜底。 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        val c = (context.applicationContext as HarukaApp).container
        val pending = goAsync()
        CoroutineScope(Dispatchers.IO).launch {
            try {
                // 未完成配置时不启动服务（否则未配置打拍即崩）。
                if (!c.settings.current().configured) return@launch
                HeartbeatService.start(context)
                c.logs.log("boot", "重启后已自启心跳服务")
            } catch (t: Throwable) {
                SyncWorker.ensureScheduled(context)
            } finally {
                pending.finish()
            }
        }
    }
}
