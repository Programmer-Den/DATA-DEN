package io.github.programmerden.dataden.sync

import java.io.File

/**
 * The photos and videos already uploaded, by their id in the phone's media
 * store, one per line in file. Appending keeps a crash from losing more than
 * the last line.
 */
class DoneLog(private val file: File) {
    private val done: MutableSet<String> = if (file.exists()) {
        file.readLines().filter { it.isNotBlank() }.toMutableSet()
    } else {
        mutableSetOf()
    }

    val size get() = done.size

    operator fun contains(key: String) = key in done

    fun add(key: String) {
        require(key.isNotBlank() && '\n' !in key)
        if (done.add(key)) file.appendText("$key\n")
    }

    fun clear() {
        done.clear()
        file.delete()
    }
}
