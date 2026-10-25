package moe.snowy.haruka.data

import android.content.ContentValues
import android.content.Context
import android.database.Cursor
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper

/** spool 中的事件行；data 为序列化后的负载 JSON。 */
data class EventRow(
    val id: String,
    val seq: Long,
    val ts: String,
    val type: String,
    val data: String,
)


/**
 * 落盘缓冲与设备内状态：观测事件与 manual 操作断网时入 spool，恢复后续传。
 * heartbeat 不入 spool（重放会虚增在线窗口）。seq 单调持久化，与桌面端同规则。
 * 容量上限 2000 行 / 7 天，超出丢最旧。
 */
class Spool(context: Context) : SQLiteOpenHelper(context, "haruka.db", null, 1) {
    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL("CREATE TABLE events(id TEXT PRIMARY KEY, seq INTEGER NOT NULL, ts TEXT NOT NULL, type TEXT NOT NULL, data TEXT NOT NULL, created_ms INTEGER NOT NULL)")
        db.execSQL("CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        db.execSQL("CREATE TABLE sync_log(n INTEGER PRIMARY KEY AUTOINCREMENT, at_ms INTEGER NOT NULL, kind TEXT NOT NULL, detail TEXT NOT NULL)")
        db.execSQL("INSERT OR IGNORE INTO meta(key, value) VALUES('seq', '0')")
    }

    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) = Unit

    /** 事务内单调取号；重启后延续。 */
    fun nextSeq(): Long {
        writableDatabase.beginTransaction()
        try {
            writableDatabase.execSQL("UPDATE meta SET value = CAST(value AS INTEGER) + 1 WHERE key = 'seq'")
            val v = readOneLong("SELECT value FROM meta WHERE key = 'seq'")
            writableDatabase.setTransactionSuccessful()
            return v
        } finally {
            writableDatabase.endTransaction()
        }
    }

    private fun readOneLong(sql: String): Long =
        readableDatabase.rawQuery(sql, null).let { c ->
            try {
                c.moveToFirst()
                c.getLong(0)
            } finally {
                c.close()
            }
        }

    fun enqueue(row: EventRow) {
        val cv = ContentValues().apply {
            put("id", row.id)
            put("seq", row.seq)
            put("ts", row.ts)
            put("type", row.type)
            put("data", row.data)
            put("created_ms", System.currentTimeMillis())
        }
        writableDatabase.insertWithOnConflict("events", null, cv, SQLiteDatabase.CONFLICT_IGNORE)
        purge()
    }

    fun count(): Int {
        val c = readableDatabase.rawQuery("SELECT COUNT(*) FROM events", null)
        try {
            c.moveToFirst()
            return c.getInt(0)
        } finally {
            c.close()
        }
    }
    /** 按入队顺序取最旧一批（快照后按序重放；单包上限由调用方控制 ≤100）。 */
    fun peek(limit: Int): List<EventRow> {
        val rows = mutableListOf<EventRow>()
        val c: Cursor = readableDatabase.rawQuery(
            "SELECT id, seq, ts, type, data FROM events ORDER BY rowid ASC LIMIT ?",
            arrayOf(limit.toString()),
        )
        try {
            while (c.moveToNext()) {
                rows.add(EventRow(c.getString(0), c.getLong(1), c.getString(2), c.getString(3), c.getString(4)))
            }
        } finally {
            c.close()
        }
        return rows
    }

    fun delete(ids: List<String>) {
        if (ids.isEmpty()) return
        val placeholders = ids.joinToString(",") { "?" }
        writableDatabase.execSQL(
            "DELETE FROM events WHERE id IN ($placeholders)",
            ids.toTypedArray(),
        )
    }

    /** 设备暂停时清空全部待报事件（设计案 §6.4：暂停即清空，恢复后从当下重新开始）。 */
    fun clearAll() {
        writableDatabase.execSQL("DELETE FROM events")
    }

    /** 超出 7 天或 2000 行时丢最旧（设计案 8.3 节口径）。 */
    private fun purge() {
        val cutoff = System.currentTimeMillis() - 7L * 24 * 3600 * 1000
        writableDatabase.execSQL("DELETE FROM events WHERE created_ms < ?", arrayOf(cutoff))
        writableDatabase.execSQL(
            "DELETE FROM events WHERE rowid NOT IN (SELECT rowid FROM events ORDER BY rowid DESC LIMIT 2000)",
        )
    }
}
