package io.github.programmerden.dataden.sync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.time.ZoneId

class PathsTest {
    @get:Rule
    val tmp = TemporaryFolder()

    @Test
    fun normalizesFolders() {
        assertEquals("/Fotos/Handy", normalizeFolder(" Fotos/Handy/ "))
        assertEquals("/Fotos", normalizeFolder("/Fotos"))
        assertNull(normalizeFolder(""))
        assertNull(normalizeFolder("/"))
        assertNull(normalizeFolder("Fotos/../etc"))
        assertNull(normalizeFolder("Fotos//Handy"))
        assertNull(normalizeFolder(".data-den-uploads"))
        assertNull(normalizeFolder("a\\b"))
    }

    @Test
    fun sortsByMonthTaken() {
        // 2026-11-01 00:30 in Berlin is still October in UTC.
        val taken = 1_793_489_400_000L
        assertEquals("/Fotos/2026/11/IMG_1.jpg", targetPath("/Fotos", "IMG_1.jpg", taken, ZoneId.of("Europe/Berlin")))
        assertEquals("/Fotos/2026/10/IMG_1.jpg", targetPath("/Fotos", "IMG_1.jpg", taken, ZoneId.of("UTC")))
    }

    @Test
    fun makesNamesSafe() {
        assertEquals("a_b.jpg", safeName("a/b.jpg"))
        assertEquals("_.data-den-x", safeName(".data-den-x"))
        assertEquals("Datei", safeName(".."))
        val long = "ä".repeat(200) + ".jpg"
        val safe = safeName(long)
        assertTrue(safe.endsWith(".jpg"))
        assertTrue(safe.toByteArray().size <= 255)
    }

    @Test
    fun numbersNames() {
        assertEquals("/F/a.jpg", numbered("/F/a.jpg", 1))
        assertEquals("/F/a (2).jpg", numbered("/F/a.jpg", 2))
        assertEquals("/F/README (3)", numbered("/F/README", 3))
        assertEquals("/F/.hidden (2)", numbered("/F/.hidden", 2))
    }

    @Test
    fun acceptsOnlyHttps() {
        assertEquals("https://nas.example.org/", serverUrl("nas.example.org").toString())
        assertEquals("https://nas.example.org/", serverUrl(" https://nas.example.org ").toString())
        assertEquals("https://nas.example.org:8443/nas/", serverUrl("https://nas.example.org:8443/nas").toString())
        assertThrows(IllegalArgumentException::class.java) { serverUrl("http://nas.example.org") }
        assertThrows(IllegalArgumentException::class.java) { serverUrl("") }
        assertThrows(IllegalArgumentException::class.java) { serverUrl("https://user:pw@nas.example.org") }
    }

    @Test
    fun remembersDoneFiles() {
        val file = tmp.newFile("done.txt")
        val log = DoneLog(file)
        log.add("images:1")
        log.add("images:1")
        log.add("video:7")
        val again = DoneLog(file)
        assertEquals(2, again.size)
        assertTrue("video:7" in again)
        assertFalse("images:2" in again)
        again.clear()
        assertEquals(0, DoneLog(file).size)
    }
}
