package io.github.programmerden.dataden.sync

import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.io.ByteArrayInputStream
import kotlin.random.Random

class UploaderTest {
    private val fake = FakeServer()
    private lateinit var client: DataDenClient

    @Before
    fun setUp() {
        client = fake.start()
    }

    @After
    fun tearDown() {
        fake.server.close()
    }

    private fun uploader() = Uploader(client, chunkSize = 1000, sleep = {})

    private fun upload(target: String, data: ByteArray, uploader: Uploader = uploader()) =
        uploader.upload(target, data.size.toLong(), { ByteArrayInputStream(data) })

    @Test
    fun uploadsInChunks() {
        val data = Random.nextBytes(3500)
        val progress = mutableListOf<Long>()
        val outcome = uploader().upload("/Fotos/a.jpg", data.size.toLong(), { ByteArrayInputStream(data) }) {
            progress += it
        }
        assertEquals(Outcome.Uploaded("/Fotos/a.jpg"), outcome)
        assertArrayEquals(data, fake.files["/Fotos/a.jpg"])
        assertEquals(listOf(1000L, 2000L, 3000L, 3500L), progress)
        assertEquals(4, fake.puts)
    }

    @Test
    fun uploadsAnEmptyFile() {
        assertEquals(Outcome.Uploaded("/a.txt"), upload("/a.txt", ByteArray(0)))
        assertEquals(0, fake.files["/a.txt"]!!.size)
    }

    @Test
    fun resumesAfterAGatewayError() {
        val data = Random.nextBytes(2500)
        fake.failPuts = 2
        assertEquals(Outcome.Uploaded("/a.jpg"), upload("/a.jpg", data))
        assertArrayEquals(data, fake.files["/a.jpg"])
    }

    @Test
    fun givesUpAfterTooManyErrors() {
        fake.brokenPuts = 100
        val e = assertThrows(ApiException::class.java) { upload("/a.jpg", Random.nextBytes(2500)) }
        assertEquals(502, e.status)
        // The first try and five more.
        assertEquals(6, fake.puts)
    }

    @Test
    fun startsOverWhenTheServerForgotTheUpload() {
        val data = Random.nextBytes(2500)
        fake.restartAfterPuts = 1
        assertEquals(Outcome.Uploaded("/a.jpg"), upload("/a.jpg", data))
        assertArrayEquals(data, fake.files["/a.jpg"])
    }

    @Test
    fun skipsTheSameFile() {
        val data = Random.nextBytes(1500)
        fake.files["/Fotos/a.jpg"] = data
        assertEquals(Outcome.AlreadyThere("/Fotos/a.jpg"), upload("/Fotos/a.jpg", data))
        assertEquals(0, fake.puts)
    }

    @Test
    fun numbersAnotherFileOfTheSameName() {
        fake.files["/Fotos/a.jpg"] = ByteArray(10)
        fake.files["/Fotos/a (2).jpg"] = ByteArray(20)
        val data = Random.nextBytes(1500)
        assertEquals(Outcome.Uploaded("/Fotos/a (3).jpg"), upload("/Fotos/a.jpg", data))
        assertArrayEquals(data, fake.files["/Fotos/a (3).jpg"])
    }

    @Test
    fun failsWhenTheFileGotShorter() {
        assertThrows(FileChangedException::class.java) {
            uploader().upload("/a.jpg", 2500, { ByteArrayInputStream(ByteArray(1200)) })
        }
    }

    @Test
    fun aWrongTokenIsUnauthorized() {
        val other = DataDenClient(fake.server.url("/"), "dd_wrong")
        assertThrows(UnauthorizedException::class.java) { other.username() }
        assertThrows(UnauthorizedException::class.java) {
            Uploader(other, sleep = {}).upload("/a.jpg", 1, { ByteArrayInputStream(ByteArray(1)) })
        }
    }

    @Test
    fun readsTheUsername() {
        assertEquals("anna", client.username())
    }

    @Test
    fun keepsTheAuthorizationOnEveryRequest() {
        upload("/a.jpg", Random.nextBytes(1500))
        repeat(fake.server.requestCount) {
            assertTrue(fake.server.takeRequest().headers["Authorization"] == "Bearer ${fake.token}")
        }
    }
}
