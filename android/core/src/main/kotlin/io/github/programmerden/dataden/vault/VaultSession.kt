package io.github.programmerden.dataden.vault

import java.io.InputStream
import java.io.IOException
import java.io.OutputStream

/** An entry of the current folder with its name opened; null when it does not open. */
data class VaultItem(val sealed: String, val name: String?, val dir: Boolean, val size: Long, val modified: String) {
    /** What the list shows. */
    val label: String get() = name ?: "(unlesbarer Name)"
}

/**
 * An open vault and the folder looked at, for the app's screen. Not thread
 * safe: use it from one background thread.
 */
class VaultSession(private val api: VaultApi, private val vault: Vault) : AutoCloseable {
    // The path from the vault's top: sealed and plain names.
    private val folders = ArrayList<Pair<String, String>>()

    /** The current folder with plain names, such as "/Steuern/2025". */
    val path: String get() = "/" + folders.joinToString("/") { it.second }

    val atTop: Boolean get() = folders.isEmpty()

    override fun close() = vault.close()

    private fun remote(sealed: String? = null): String =
        "/" + (folders.map { it.first } + listOfNotNull(sealed)).joinToString("/")

    /** The current folder, folders first, by name. */
    fun list(): List<VaultItem> = api.list(remote()).map { e ->
        VaultItem(e.name, vault.openName(e.name), e.dir, if (e.dir) 0 else Vault.plainSize(e.size), e.modified)
    }.sortedWith(compareBy<VaultItem>({ !it.dir }, { it.label.lowercase() }))

    fun enter(item: VaultItem) {
        require(item.dir && item.name != null)
        folders += item.sealed to item.name
    }

    /** One folder up; false at the top. */
    fun up(): Boolean = folders.removeLastOrNull() != null

    private fun checkNew(name: String) {
        Vault.checkName(name)?.let { throw IOException(it) }
        if (list().any { it.name == name }) throw IOException("$name gibt es hier schon")
    }

    /** Encrypts size bytes from input into a new file name in the current folder. */
    fun upload(name: String, size: Long, input: InputStream) {
        if (size > Vault.MAX_PLAIN) throw IOException("Höchstens 1 GB je Datei im Tresor")
        checkNew(name)
        api.upload(remote(vault.sealName(name)), Vault.sealedSize(size)) { vault.seal(input, size, it) }
    }

    /**
     * Decrypts item into output. DamagedException when it does not decrypt in
     * full; output then holds a part that must be thrown away.
     */
    fun download(item: VaultItem, output: OutputStream) {
        require(!item.dir)
        api.download(remote(item.sealed)) { vault.unseal(it, output) }
    }

    fun mkdir(name: String) {
        checkNew(name)
        api.mkdir(remote(vault.sealName(name)))
    }

    fun rename(item: VaultItem, to: String) {
        checkNew(to)
        api.move(remote(item.sealed), remote(vault.sealName(to)))
    }

    /** Deletes item for good, a folder with everything in it. */
    fun delete(item: VaultItem) = api.delete(remote(item.sealed))
}
