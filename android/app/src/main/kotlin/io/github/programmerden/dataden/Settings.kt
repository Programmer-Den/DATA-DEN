package io.github.programmerden.dataden

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** What the person set up, kept in the app's private storage. */
class Settings(context: Context) {
    private val prefs = context.getSharedPreferences("settings", Context.MODE_PRIVATE)

    var server: String
        get() = prefs.getString("server", "").orEmpty()
        set(value) = prefs.edit().putString("server", value).apply()

    var username: String
        get() = prefs.getString("username", "").orEmpty()
        set(value) = prefs.edit().putString("username", value).apply()

    var folder: String
        get() = prefs.getString("folder", DEFAULT_FOLDER) ?: DEFAULT_FOLDER
        set(value) = prefs.edit().putString("folder", value).apply()

    var videos: Boolean
        get() = prefs.getBoolean("videos", true)
        set(value) = prefs.edit().putBoolean("videos", value).apply()

    var cameraOnly: Boolean
        get() = prefs.getBoolean("cameraOnly", true)
        set(value) = prefs.edit().putBoolean("cameraOnly", value).apply()

    var wifiOnly: Boolean
        get() = prefs.getBoolean("wifiOnly", true)
        set(value) = prefs.edit().putBoolean("wifiOnly", value).apply()

    var chargingOnly: Boolean
        get() = prefs.getBoolean("chargingOnly", false)
        set(value) = prefs.edit().putBoolean("chargingOnly", value).apply()

    /** Only media added at or after this time (seconds) is uploaded; 0 for all. */
    var since: Long
        get() = prefs.getLong("since", 0)
        set(value) = prefs.edit().putLong("since", value).apply()

    /** The app password, encrypted with a key that never leaves the phone's keystore. */
    var token: String?
        get() = prefs.getString("token", null)?.let { TokenCipher.decrypt(it) }
        set(value) {
            if (value == null) {
                prefs.edit().remove("token").apply()
            } else {
                prefs.edit().putString("token", TokenCipher.encrypt(value)).apply()
            }
        }

    val connected: Boolean get() = server.isNotEmpty() && prefs.contains("token")

    fun signOut() {
        prefs.edit().remove("token").remove("username").apply()
    }

    companion object {
        const val DEFAULT_FOLDER = "/Fotos/Handy"
    }
}

/** What the uploads did, for the app's screen. */
class Status(context: Context) {
    val prefs = context.getSharedPreferences("status", Context.MODE_PRIVATE)

    val uploaded: Int get() = prefs.getInt("uploaded", 0)
    val lastRun: Long get() = prefs.getLong("lastRun", 0)
    val message: String get() = prefs.getString("message", "").orEmpty()

    fun countUpload() = prefs.edit().putInt("uploaded", uploaded + 1).apply()

    fun finished(message: String = "") =
        prefs.edit().putLong("lastRun", System.currentTimeMillis()).putString("message", message).apply()

    fun reset() = prefs.edit().clear().apply()
}

/** AES-GCM with a key in the Android keystore. */
private object TokenCipher {
    private const val ALIAS = "data-den-token"
    private const val TRANSFORMATION = "AES/GCM/NoPadding"

    fun encrypt(plain: String): String {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val sealed = cipher.iv + cipher.doFinal(plain.toByteArray())
        return Base64.encodeToString(sealed, Base64.NO_WRAP)
    }

    /** Null when the key is gone, for example after a restore: sign in again. */
    fun decrypt(stored: String): String? = runCatching {
        val sealed = Base64.decode(stored, Base64.NO_WRAP)
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, sealed, 0, 12))
        String(cipher.doFinal(sealed, 12, sealed.size - 12))
    }.getOrNull()

    private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        generator.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return generator.generateKey()
    }
}
