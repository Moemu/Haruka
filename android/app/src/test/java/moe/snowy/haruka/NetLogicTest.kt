package moe.snowy.haruka

import moe.snowy.haruka.net.normalizeBaseUrl
import kotlin.test.Test
import kotlin.test.assertEquals

/** 向导服务器地址归一化：无 scheme 补 https，显式 scheme 原样保留（本地调试走 http://）。 */
class NetLogicTest {
    @Test
    fun `schemeless url gets https`() {
        assertEquals("https://haruka.snowy.moe", normalizeBaseUrl("haruka.snowy.moe"))
        assertEquals("https://haruka.snowy.moe", normalizeBaseUrl("  haruka.snowy.moe/  "))
        assertEquals("https://10.0.2.2:8099", normalizeBaseUrl("10.0.2.2:8099/"))
    }

    @Test
    fun `explicit scheme is kept`() {
        assertEquals("http://10.0.2.2:8099", normalizeBaseUrl("http://10.0.2.2:8099"))
        assertEquals("https://a.example.com", normalizeBaseUrl(" https://a.example.com// "))
    }
}
