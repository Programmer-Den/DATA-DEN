package io.github.programmerden.dataden

import android.content.Context
import android.os.Handler
import android.os.Looper
import io.github.programmerden.dataden.vault.VaultSession
import java.io.File
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

/**
 * The open vault (Tresor), kept only in this process's memory: leaving the
 * screen for another app keeps it open, but the lock button, 15 minutes
 * without use or the end of the process lock it. Decrypted copies for other
 * apps live in the cache under [dir] and are deleted when it locks.
 */
object Tresor {
    /** How long the vault stays open without anything done in it, as on the website. */
    private const val IDLE_LOCK = 15 * 60 * 1000L

    private val main = Handler(Looper.getMainLooper())
    private val lockLater = Runnable { lock() }
    private var copies: File? = null

    /** The open vault; touch it only on [worker]. */
    @Volatile
    var session: VaultSession? = null
        private set

    /** Runs everything that talks to the server or decrypts, one at a time. */
    val worker: ExecutorService = Executors.newSingleThreadExecutor()

    /** Called on the main thread after the vault locked. */
    var onLocked: (() -> Unit)? = null

    /** Where decrypted copies for other apps are kept until the vault locks. */
    fun dir(context: Context) = File(context.cacheDir, "tresor")

    fun open(context: Context, opened: VaultSession) {
        copies = dir(context)
        session = opened
        touch()
    }

    /** Something was done in the vault: the idle time starts again. */
    fun touch() {
        main.removeCallbacks(lockLater)
        if (session != null) main.postDelayed(lockLater, IDLE_LOCK)
    }

    /** Forgets the key and deletes the decrypted copies. */
    fun lock() {
        main.removeCallbacks(lockLater)
        val closing = session
        session = null
        val dir = copies
        worker.execute {
            closing?.close()
            dir?.deleteRecursively()
        }
        if (closing != null) onLocked?.invoke()
    }

    /** Deletes copies a process that ended without locking left behind. */
    fun cleanUp(context: Context) {
        if (session == null) {
            val dir = dir(context)
            worker.execute { dir.deleteRecursively() }
        }
    }
}
