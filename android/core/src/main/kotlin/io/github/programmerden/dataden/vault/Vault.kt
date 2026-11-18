package io.github.programmerden.dataden.vault

import kotlinx.serialization.Serializable
import java.io.EOFException
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.nio.ByteBuffer
import java.nio.charset.CharacterCodingException
import java.nio.charset.CodingErrorAction
import java.security.GeneralSecurityException
import java.security.SecureRandom
import java.util.Base64
import javax.crypto.Cipher
import javax.crypto.Mac
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.PBEKeySpec
import javax.crypto.spec.SecretKeySpec

/*
 * The vault (Tresor) in the same format as the website's
 * frontend/src/app/vault/vault-crypto.ts, so the app opens what the browser
 * sealed and the other way round. Only the platform's crypto (PBKDF2,
 * HMAC-SHA256 for HKDF, AES-256-GCM):
 *
 * - The vault key is 32 random bytes, stored on the server wrapped with
 *   AES-256-GCM: once under a key from the passphrase (PBKDF2-SHA256) and
 *   once under one from the recovery key (HKDF-SHA256).
 * - Names are sealed with AES-256-GCM under a key derived from the vault
 *   key: base64url(nonce || ciphertext || tag).
 * - Files use the STREAM construction: a key per file from HKDF-SHA256 with
 *   a random 32-byte salt, then 1 MiB chunks sealed with AES-256-GCM under
 *   the nonce counter (11 bytes, big endian) || last flag (1 byte).
 *   Layout: "DDV1" || salt || chunks.
 */

/** What the server keeps: the vault key, wrapped twice. */
@Serializable
data class VaultKeys(val version: Int, val passphrase: PassphraseKey, val recovery: RecoveryKey)

@Serializable
data class PassphraseKey(val kdf: String, val iterations: Int, val salt: String, val iv: String, val wrapped: String)

@Serializable
data class RecoveryKey(val kdf: String, val salt: String, val iv: String, val wrapped: String)

/** The passphrase or recovery key does not open the vault. */
class WrongSecretException : GeneralSecurityException("Passphrase oder Wiederherstellungsschlüssel stimmt nicht")

/** A file does not decrypt in full: damaged, cut off or from another vault. */
class DamagedException : IOException("Die Datei lässt sich nicht entschlüsseln: beschädigt oder aus einem anderen Tresor")

/** An open vault. Its keys stay in memory only; close forgets them. */
class Vault private constructor(private val master: ByteArray) : AutoCloseable {
    private val names = SecretKeySpec(hkdf(master, ByteArray(0), "data-den vault names".toByteArray(), 32), "AES")
    private val random = SecureRandom()

    override fun close() = master.fill(0)

    /** The name as the server stores it; throws for a name the vault cannot take. */
    fun sealName(name: String): String {
        checkName(name)?.let { throw IllegalArgumentException(it) }
        val nonce = ByteArray(NONCE).also(random::nextBytes)
        val cipher = Cipher.getInstance(GCM).apply { init(Cipher.ENCRYPT_MODE, names, GCMParameterSpec(128, nonce)) }
        return Base64.getUrlEncoder().withoutPadding().encodeToString(nonce + cipher.doFinal(name.toByteArray()))
    }

    /** The name behind a sealed one; null when it does not open. */
    fun openName(sealed: String): String? = try {
        val bytes = Base64.getUrlDecoder().decode(sealed)
        if (bytes.size < NONCE + TAG || sealed.contains('=')) {
            null
        } else {
            val cipher = Cipher.getInstance(GCM).apply {
                init(Cipher.DECRYPT_MODE, names, GCMParameterSpec(128, bytes, 0, NONCE))
            }
            strictUtf8(cipher.doFinal(bytes, NONCE, bytes.size - NONCE))
        }
    } catch (_: IllegalArgumentException) {
        null
    } catch (_: GeneralSecurityException) {
        null
    } catch (_: CharacterCodingException) {
        null
    }

    private fun fileKey(salt: ByteArray) = SecretKeySpec(hkdf(master, salt, "data-den vault file".toByteArray(), 32), "AES")

    /** Writes the size bytes from input to output, sealed; input must give exactly size bytes. */
    fun seal(input: InputStream, size: Long, output: OutputStream) {
        require(size >= 0)
        val salt = ByteArray(SALT).also(random::nextBytes)
        val key = fileKey(salt)
        output.write(MAGIC)
        output.write(salt)
        val buf = ByteArray(CHUNK)
        var left = size
        var counter = 0
        while (true) {
            val n = minOf(left, CHUNK.toLong()).toInt()
            readFully(input, buf, n)
            left -= n
            val cipher = Cipher.getInstance(GCM)
            cipher.init(Cipher.ENCRYPT_MODE, key, GCMParameterSpec(128, chunkNonce(counter, left == 0L)))
            output.write(cipher.doFinal(buf, 0, n))
            if (left == 0L) return
            counter++
        }
    }

    /**
     * Writes the file sealed in input to output. Each chunk is checked before
     * it is written; DamagedException when the file does not decrypt in full,
     * and then output holds only a part of it, which must be thrown away.
     */
    fun unseal(input: InputStream, output: OutputStream) {
        val header = ByteArray(MAGIC.size + SALT)
        if (read(input, header, 0, header.size) != header.size || !header.copyOf(MAGIC.size).contentEquals(MAGIC)) {
            throw DamagedException()
        }
        val key = fileKey(header.copyOfRange(MAGIC.size, header.size))
        // One byte more than a chunk shows whether another chunk follows.
        val buf = ByteArray(CHUNK + TAG + 1)
        var n = read(input, buf, 0, buf.size)
        var counter = 0
        while (true) {
            val last = n < buf.size
            val size = minOf(n, CHUNK + TAG)
            if (size < TAG) throw DamagedException()
            val plain = try {
                val cipher = Cipher.getInstance(GCM)
                cipher.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, chunkNonce(counter, last)))
                cipher.doFinal(buf, 0, size)
            } catch (_: GeneralSecurityException) {
                throw DamagedException()
            }
            output.write(plain)
            if (last) return
            counter++
            // The extra byte starts the next chunk.
            buf[0] = buf[size]
            n = 1 + read(input, buf, 1, buf.size - 1)
        }
    }

    companion object {
        /** How much of a file one sealed chunk holds. */
        const val CHUNK = 1 shl 20

        /** The longest name in UTF-8 bytes, so the sealed name fits in 255 characters. */
        const val MAX_NAME_BYTES = 160

        /** The largest file the server takes into the vault. */
        const val MAX_PLAIN = 1L shl 30

        private val MAGIC = "DDV1".toByteArray()
        private const val SALT = 32
        private const val TAG = 16
        private const val NONCE = 12
        private const val GCM = "AES/GCM/NoPadding"

        // As in the browser; the upper bound keeps forged keys from stalling the phone.
        private const val MIN_ITERATIONS = 100_000
        private const val MAX_ITERATIONS = 10_000_000

        /**
         * Opens the vault with secret: as a recovery key when it reads as one,
         * otherwise, or when that does not fit, as the passphrase. Slow (the
         * passphrase takes 600 000 rounds of PBKDF2): call it off the main thread.
         */
        fun open(keys: VaultKeys, secret: String): Vault {
            if (keys.version != 1) throw GeneralSecurityException("Unbekanntes Schlüsselformat ${keys.version}")
            parseRecovery(secret)?.let { recovery ->
                try {
                    return Vault(openRecovery(keys.recovery, recovery))
                } catch (_: WrongSecretException) {
                    // Perhaps a passphrase that looks like a recovery key.
                } finally {
                    recovery.fill(0)
                }
            }
            return Vault(openPassphrase(keys.passphrase, secret))
        }

        private fun openPassphrase(p: PassphraseKey, passphrase: String): ByteArray {
            if (p.kdf != "PBKDF2-SHA256" || p.iterations !in MIN_ITERATIONS..MAX_ITERATIONS) {
                throw GeneralSecurityException("Unbekanntes Schlüsselformat")
            }
            // The platform encodes the characters as UTF-8, as the browser does.
            val spec = PBEKeySpec(passphrase.toCharArray(), decode(p.salt), p.iterations, 256)
            val kek = try {
                SecretKeyFactory.getInstance("PBKDF2WithHmacSHA256").generateSecret(spec).encoded
            } finally {
                spec.clearPassword()
            }
            return unwrap(kek, p.iv, p.wrapped)
        }

        private fun openRecovery(r: RecoveryKey, recovery: ByteArray): ByteArray {
            if (r.kdf != "HKDF-SHA256") throw GeneralSecurityException("Unbekanntes Schlüsselformat")
            return unwrap(hkdf(recovery, decode(r.salt), "data-den vault recovery".toByteArray(), 32), r.iv, r.wrapped)
        }

        private fun decode(s: String): ByteArray = try {
            Base64.getDecoder().decode(s)
        } catch (_: IllegalArgumentException) {
            throw GeneralSecurityException("Unbekanntes Schlüsselformat")
        }

        private fun unwrap(kek: ByteArray, iv: String, wrapped: String): ByteArray {
            try {
                val nonce = decode(iv)
                if (nonce.size != NONCE) throw WrongSecretException()
                val cipher = Cipher.getInstance(GCM)
                cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(kek, "AES"), GCMParameterSpec(128, nonce))
                val raw = cipher.doFinal(decode(wrapped))
                if (raw.size != 32) throw WrongSecretException()
                return raw
            } catch (e: WrongSecretException) {
                throw e
            } catch (_: GeneralSecurityException) {
                throw WrongSecretException()
            } finally {
                kek.fill(0)
            }
        }

        /** Why name cannot go into the vault, or null when it can. */
        fun checkName(name: String): String? = when {
            name.isEmpty() || name == "." || name == ".." -> "Ungültiger Name"
            name.toByteArray().size > MAX_NAME_BYTES -> "Namen im Tresor dürfen höchstens $MAX_NAME_BYTES Bytes lang sein"
            name.any { it == '/' || it == '\\' || it == '\u0000' } -> "Ungültiger Name"
            else -> null
        }

        /** The size of a file of size bytes once sealed. */
        fun sealedSize(size: Long): Long {
            val chunks = maxOf(1L, (size + CHUNK - 1) / CHUNK)
            return MAGIC.size + SALT + size + TAG * chunks
        }

        /** The size of a file before it was sealed, from the sealed size the server lists. */
        fun plainSize(sealed: Long): Long {
            val body = sealed - MAGIC.size - SALT
            return maxOf(0L, body - TAG * ((body + CHUNK + TAG - 1) / (CHUNK + TAG)))
        }

        private fun chunkNonce(counter: Int, last: Boolean): ByteArray {
            val nonce = ByteArray(NONCE)
            ByteBuffer.wrap(nonce).putInt(7, counter)
            nonce[11] = if (last) 1 else 0
            return nonce
        }

        /** Reads up to len bytes, fewer only at the end of input. */
        private fun read(input: InputStream, buf: ByteArray, off: Int, len: Int): Int {
            var total = 0
            while (total < len) {
                val n = input.read(buf, off + total, len - total)
                if (n < 0) break
                total += n
            }
            return total
        }

        private fun readFully(input: InputStream, buf: ByteArray, len: Int) {
            if (read(input, buf, 0, len) != len) throw EOFException("Die Datei wurde beim Lesen kürzer")
        }

        private fun strictUtf8(bytes: ByteArray): String = Charsets.UTF_8.newDecoder()
            .onMalformedInput(CodingErrorAction.REPORT)
            .onUnmappableCharacter(CodingErrorAction.REPORT)
            .decode(ByteBuffer.wrap(bytes)).toString()

        // Crockford's base32, for a recovery key that can be typed.
        private const val BASE32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

        /** The 32 bytes of a typed recovery key, such as "ABCD-EFGH-...", or null when it is not one. */
        fun parseRecovery(text: String): ByteArray? {
            val chars = text.uppercase().filter { !it.isWhitespace() && it != '-' }
                .replace('I', '1').replace('L', '1').replace('O', '0')
            if (chars.length != 52) return null
            val out = ByteArray(32)
            var i = 0
            var value = 0
            var bits = 0
            for (c in chars) {
                val v = BASE32.indexOf(c)
                if (v < 0) return null
                value = ((value shl 5) or v) and 0xffff
                bits += 5
                if (bits >= 8) {
                    if (i == 32) return null
                    out[i++] = (value ushr (bits - 8)).toByte()
                    bits -= 8
                }
            }
            // 52 characters hold 260 bits: the last 4 must be zero.
            return if (i == 32 && value and ((1 shl bits) - 1) == 0) out else null
        }

        /**
         * HKDF-SHA256 (RFC 5869) over the platform's HMAC, as WebCrypto and
         * Go's crypto/hkdf compute it. An empty salt is HashLen zero bytes,
         * as the RFC says.
         */
        internal fun hkdf(ikm: ByteArray, salt: ByteArray, info: ByteArray, length: Int): ByteArray {
            require(length in 1..255 * 32)
            val mac = Mac.getInstance("HmacSHA256")
            mac.init(SecretKeySpec(if (salt.isEmpty()) ByteArray(32) else salt, "HmacSHA256"))
            val prk = mac.doFinal(ikm)
            mac.init(SecretKeySpec(prk, "HmacSHA256"))
            prk.fill(0)
            val out = ByteArray(length)
            var t = ByteArray(0)
            var done = 0
            var block = 1
            while (done < length) {
                mac.update(t)
                mac.update(info)
                mac.update(block.toByte())
                t = mac.doFinal()
                val n = minOf(t.size, length - done)
                System.arraycopy(t, 0, out, done, n)
                done += n
                block++
            }
            return out
        }
    }
}
