package moe.snowy.haruka

import moe.snowy.haruka.sync.isSyncStale
import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** 观测回路阈值（spec §5.4）：3 × 登记间隔，边界行为在这里锁定。 */
class SyncLogicTest {
    private val intervalMs = 15L * 60_000

    @Test
    fun `stale only after three intervals`() {
        val last = 1_000_000L
        assertFalse(isSyncStale(last + intervalMs * 3, last, 15), "恰好 3 倍不提示")
        assertTrue(isSyncStale(last + intervalMs * 3 + 1, last, 15), "超过 3 倍提示")
    }

    @Test
    fun `never stale without a previous sync or interval`() {
        val now = 10_000_000L
        assertFalse(isSyncStale(now, 0, 15), "从未同步过不算断档")
        assertFalse(isSyncStale(now, 1, 0), "无登记间隔不算断档")
    }
}
