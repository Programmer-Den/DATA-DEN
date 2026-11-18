package io.github.programmerden.dataden.vault

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.security.GeneralSecurityException
import java.util.Base64

/**
 * vault-web.json is a vault the website's vault-crypto.ts made (with 100 000
 * PBKDF2 rounds to keep the test quick), so the app opens what the browser
 * sealed; unicodeKeys are the same vault with a passphrase beyond ASCII.
 */
@Serializable
data class WebVault(
    val passphrase: String,
    val recovery: String,
    val keys: VaultKeys,
    val unicodePassphrase: String,
    val unicodeKeys: VaultKeys,
    val names: Map<String, String>,
    val files: Map<String, WebFile>,
)

@Serializable
data class WebFile(val plain: String, val sealed: String)

fun webVault(): WebVault {
    val text = WebVault::class.java.getResource("/vault-web.json")!!.readText()
    return Json { ignoreUnknownKeys = true }.decodeFromString(text)
}

class VaultTest {
    private val web = webVault()
    private val b64 = Base64.getDecoder()

    private fun open() = Vault.open(web.keys, web.passphrase)

    @Test
    fun opensWithPassphraseRecoveryAndUnicode() {
        val sealed = web.names.getValue("Steuer 2025.pdf")
        for ((keys, secret) in listOf(
            web.keys to web.passphrase,
            web.keys to web.recovery.lowercase().replace("-", " "),
            web.unicodeKeys to web.unicodePassphrase,
        )) {
            assertEquals(secret, "Steuer 2025.pdf", Vault.open(keys, secret).openName(sealed))
        }
    }

    @Test
    fun refusesWrongSecrets() {
        for (wrong in listOf("", "falsch", web.passphrase + " ", web.recovery.replaceFirst('N', 'M'))) {
            assertThrows(wrong, WrongSecretException::class.java) { Vault.open(web.keys, wrong) }
        }
        val few = web.keys.copy(passphrase = web.keys.passphrase.copy(iterations = 1000))
        val e = assertThrows(GeneralSecurityException::class.java) { Vault.open(few, web.passphrase) }
        assertTrue(e !is WrongSecretException)
    }

    @Test
    fun namesFromTheBrowser() {
        val vault = open()
        for ((name, sealed) in web.names) {
            assertEquals(name, vault.openName(sealed))
            val again = vault.sealName(name)
            assertNotEquals(sealed, again)
            assertEquals(name, vault.openName(again))
        }
        val sealed = web.names.getValue("Steuer 2025.pdf")
        for (bad in listOf("", "!!", sealed.take(30), sealed + "A", "$sealed==")) {
            assertNull(bad, vault.openName(bad))
        }
        for (bad in listOf("", ".", "..", "a/b", "a\\b", "x".repeat(Vault.MAX_NAME_BYTES + 1))) {
            assertThrows(bad, IllegalArgumentException::class.java) { vault.sealName(bad) }
        }
        assertTrue(vault.sealName("ä".repeat(Vault.MAX_NAME_BYTES / 2)).length <= 255)
    }

    @Test
    fun filesFromTheBrowser() {
        val vault = open()
        for ((name, file) in web.files) {
            val plain = b64.decode(file.plain)
            val sealed = b64.decode(file.sealed)
            val out = ByteArrayOutputStream()
            vault.unseal(ByteArrayInputStream(sealed), out)
            assertArrayEquals(name, plain, out.toByteArray())
            assertEquals(name, plain.size.toLong(), Vault.plainSize(sealed.size.toLong()))
            assertEquals(name, sealed.size.toLong(), Vault.sealedSize(plain.size.toLong()))
        }
    }

    @Test
    fun sealsAndUnseals() {
        val vault = open()
        val chunk = Vault.CHUNK
        for (size in listOf(0, 1, chunk - 1, chunk, chunk + 1, 2 * chunk, 2 * chunk + chunk / 2)) {
            val plain = ByteArray(size) { (it * 7 + (it shr 11)).toByte() }
            val sealed = ByteArrayOutputStream().also { vault.seal(ByteArrayInputStream(plain), size.toLong(), it) }.toByteArray()
            assertEquals(Vault.sealedSize(size.toLong()), sealed.size.toLong())
            assertEquals(size.toLong(), Vault.plainSize(sealed.size.toLong()))
            val out = ByteArrayOutputStream().also { vault.unseal(ByteArrayInputStream(sealed), it) }
            assertArrayEquals("$size bytes", plain, out.toByteArray())
        }
    }

    @Test
    fun noticesDamage() {
        val vault = open()
        val plain = ByteArray(2 * Vault.CHUNK + 100) { 'x'.code.toByte() }
        val sealed = ByteArrayOutputStream().also { vault.seal(ByteArrayInputStream(plain), plain.size.toLong(), it) }.toByteArray()
        val head = 36
        val chunk = Vault.CHUNK + 16
        fun flip(i: Int) = sealed.copyOf().also { it[i] = (it[i].toInt() xor 1).toByte() }
        val damaged = mapOf(
            "cut after a chunk" to sealed.copyOf(head + chunk),
            "cut in a chunk" to sealed.copyOf(sealed.size - 1),
            "last chunk gone" to sealed.copyOf(head + 2 * chunk),
            "flipped bit" to flip(head + chunk + 5),
            "other salt" to flip(10),
            "no magic" to flip(0),
            "only the header" to sealed.copyOf(head),
            "swapped chunks" to sealed.copyOfRange(0, head) + sealed.copyOfRange(head + chunk, head + 2 * chunk) +
                sealed.copyOfRange(head, head + chunk) + sealed.copyOfRange(head + 2 * chunk, sealed.size),
        )
        for ((what, bytes) in damaged) {
            assertThrows(what, DamagedException::class.java) { vault.unseal(ByteArrayInputStream(bytes), ByteArrayOutputStream()) }
        }
        val other = Vault.open(web.keys, web.passphrase).also { it.close() } // a vault key of zeros now
        assertThrows(DamagedException::class.java) { other.unseal(ByteArrayInputStream(sealed), ByteArrayOutputStream()) }
    }

    @Test
    fun parsesRecoveryKeys() {
        val key = Vault.parseRecovery(web.recovery)!!
        assertEquals(32, key.size)
        assertArrayEquals(key, Vault.parseRecovery(web.recovery.replace('0', 'o').replace('1', 'l')))
        for (bad in listOf("", web.recovery.dropLast(1), web.recovery + "0", web.recovery.replaceFirst('N', 'U'), web.recovery.dropLast(1) + "1")) {
            assertNull(bad, Vault.parseRecovery(bad))
        }
    }

    @Test
    fun hkdfMatchesRfc5869() {
        fun hex(s: String) = s.chunked(2).map { it.toInt(16).toByte() }.toByteArray()
        // Test case 1 of RFC 5869, appendix A.
        val okm = Vault.hkdf(
            ByteArray(22) { 0x0b },
            hex("000102030405060708090a0b0c"),
            hex("f0f1f2f3f4f5f6f7f8f9"),
            42,
        )
        assertArrayEquals(hex("3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865"), okm)
        // Test case 3: no salt and no info.
        val noSalt = Vault.hkdf(ByteArray(22) { 0x0b }, ByteArray(0), ByteArray(0), 42)
        assertArrayEquals(hex("8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d9d201395faa4b61a96c8"), noSalt)
    }
}
