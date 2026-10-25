package moe.snowy.haruka.data

import android.content.Context
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * 运行日志：文件存储（files/logs/haruka.log），默认不进任何界面（参考 FlClash 的安排），
 * 设置 → 高级 的诊断入口按需查看/复制/清除。超过 MAX_BYTES 轮转为 .old，只留一代。
 */
class LogStore(context: Context) {
    private val dir = File(context.filesDir, "logs")
    private val old get() = File(dir, "haruka.log.old")
    private val file get() = File(dir, "haruka.log")

    @Synchronized
    fun log(kind: String, detail: String) {
        try {
            dir.mkdirs()
            if (file.exists() && file.length() > MAX_BYTES) {
                old.delete()
                file.renameTo(old)
            }
            file.appendText("${stamp.format(Date())} [$kind] ${detail.take(300)}\n")
        } catch (_: Exception) {
            // 日志写入失败不影响业务
        }
    }

    /** 最近 maxLines 行，诊断页展示与复制共用。 */
    @Synchronized
    fun tail(maxLines: Int = 400): List<String> = try {
        file.takeIf { it.exists() }?.readLines()?.takeLast(maxLines).orEmpty()
    } catch (_: Exception) {
        emptyList()
    }

    @Synchronized
    fun clear() {
        try {
            file.delete()
            old.delete()
        } catch (_: Exception) {
        }
    }

    companion object {
        private const val MAX_BYTES = 256 * 1024
        private val stamp = SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US)
    }
}
