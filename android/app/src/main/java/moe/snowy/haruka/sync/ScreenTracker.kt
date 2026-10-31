package moe.snowy.haruka.sync

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Build
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import moe.snowy.haruka.data.SettingsRepository

/**
 * 亮屏启发式的屏幕状态源：服务运行期间监听亮屏/解锁/息屏，把最近活跃时刻
 * 持久化（重启后 presence 仍能如实报 away 的空闲时长）。采样本身无状态、无权限。
 */
class ScreenTracker(
    private val context: Context,
    private val settings: SettingsRepository,
) {
    private val scope = CoroutineScope(Dispatchers.IO)

    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(ctx: Context, intent: Intent) {
            // 亮屏与解锁都算活跃锚点；息屏只写内存，锚点仍在最近亮屏时刻。
            val now = System.currentTimeMillis()
            lastActiveCache = now
            scope.launch { settings.setLastActive(now) }
        }
    }

    fun start() {
        val filter = IntentFilter().apply {
            addAction(Intent.ACTION_SCREEN_ON)
            addAction(Intent.ACTION_USER_PRESENT)
            addAction(Intent.ACTION_SCREEN_OFF)
        }
        if (Build.VERSION.SDK_INT >= 33) {
            context.registerReceiver(receiver, filter, Context.RECEIVER_NOT_EXPORTED)
        } else {
            ContextCompat.registerReceiver(context, receiver, filter, ContextCompat.RECEIVER_NOT_EXPORTED)
        }
        // 冷启动读回持久化锚点，避免 presence 把重启前空闲清零。
        scope.launch {
            lastActiveCache = settings.current().lastActiveMs
        }
    }

    fun stop() {
        try {
            context.unregisterReceiver(receiver)
        } catch (e: IllegalArgumentException) {
            // 未注册或已注销
        }
    }

    companion object {
        // 内存缓存即真相源（receiver 写穿持久化）；进程重建时由 start() 读回。
        @Volatile
        var lastActiveCache: Long = 0
            private set
    }
}
