package io.github.programmerden.dataden.sync

import java.io.EOFException
import java.io.IOException
import java.io.InputStream

/** What became of a file. */
sealed interface Outcome {
    /** Uploaded to path. */
    data class Uploaded(val path: String) : Outcome

    /** A file of the same name and size was already at path. */
    data class AlreadyThere(val path: String) : Outcome
}

/** The file on the phone changed while it was being read; try again later. */
class FileChangedException(message: String) : IOException(message)

/**
 * Uploads files in chunks and resumes after a broken connection, the way the
 * web interface does. A name that is taken by another file gets a number,
 * "a (2).jpg"; a file of the same name and size counts as already uploaded.
 */
class Uploader(
    private val client: DataDenClient,
    private val chunkSize: Int = DEFAULT_CHUNK,
    private val retries: Int = 5,
    private val sleep: (Long) -> Unit = { Thread.sleep(it) },
) {
    init {
        require(chunkSize in 1..MAX_CHUNK)
    }

    private val buffer = ByteArray(chunkSize)

    /**
     * Uploads size bytes to target, such as "/Fotos/Handy/2026/11/a.jpg".
     * open returns the file from its start; it is called again to resume.
     * progress gets the bytes sent so far.
     */
    fun upload(target: String, size: Long, open: () -> InputStream, progress: (Long) -> Unit = {}): Outcome {
        var number = 1
        var restarts = 0
        while (true) {
            val path = numbered(target, number)
            val started = try {
                client.startUpload(path, size)
            } catch (e: ApiException) {
                if (e.status != 409) throw e
                if (sameFile(path, size)) return Outcome.AlreadyThere(path)
                if (++number > MAX_NUMBER) throw e
                continue
            }
            try {
                send(started, size, open, progress)
                return Outcome.Uploaded(path)
            } catch (e: ApiException) {
                when {
                    // The name was taken while uploading; check it like above.
                    e.status == 409 && e.offset == null -> {
                        if (sameFile(path, size)) return Outcome.AlreadyThere(path)
                        if (++number > MAX_NUMBER) throw e
                    }
                    // The server restarted and forgot the upload: start over.
                    e.status == 404 && restarts++ < retries -> {}
                    else -> throw e
                }
            }
        }
    }

    private fun send(started: Started, size: Long, open: () -> InputStream, progress: (Long) -> Unit) {
        var offset = started.offset
        var failures = 0
        var stream: InputStream? = null
        try {
            while (offset < size) {
                if (stream == null) stream = open().also { skipFully(it, offset) }
                val length = minOf(chunkSize.toLong(), size - offset).toInt()
                readFully(stream, length)
                val next = try {
                    client.putChunk(started.id, offset, buffer, length)
                } catch (e: IOException) {
                    if (++failures > retries) throw e
                    stream.close()
                    stream = null
                    when {
                        // The server has another offset, for example after a
                        // timeout whose chunk did arrive: go on from there.
                        e is ApiException && e.status in RESUMABLE && e.offset != null -> e.offset
                        e is ApiException && e.status !in GATEWAY -> throw e
                        // The connection or the Pi in front broke: wait, then ask.
                        else -> {
                            sleep(BACKOFF_MILLIS shl (failures - 1))
                            resumeOffset(started.id) ?: offset
                        }
                    }
                }
                if (next == offset + length) failures = 0
                offset = next
                progress(offset)
            }
        } finally {
            stream?.close()
        }
    }

    /** The server's offset, or null when it cannot be asked right now. */
    private fun resumeOffset(id: String): Long? = try {
        client.uploadOffset(id)
    } catch (e: ApiException) {
        if (e.status !in GATEWAY) throw e
        null
    } catch (_: IOException) {
        null
    }

    private fun sameFile(path: String, size: Long): Boolean {
        val name = path.substringAfterLast('/')
        val entries = try {
            client.list(path.substringBeforeLast('/').ifEmpty { "/" })
        } catch (e: ApiException) {
            if (e.status == 404) return false
            throw e
        }
        return entries.any { !it.dir && it.name == name && it.size == size }
    }

    private fun readFully(stream: InputStream, length: Int) {
        var read = 0
        while (read < length) {
            val n = stream.read(buffer, read, length - read)
            if (n < 0) throw FileChangedException("Die Datei ist kürzer geworden")
            read += n
        }
    }

    private fun skipFully(stream: InputStream, count: Long) {
        try {
            stream.skipNBytesCompat(count)
        } catch (_: EOFException) {
            throw FileChangedException("Die Datei ist kürzer geworden")
        }
    }

    companion object {
        /** The size of one PUT; the server takes at most MAX_CHUNK. */
        const val DEFAULT_CHUNK = 8 shl 20
        const val MAX_CHUNK = 32 shl 20
        private const val MAX_NUMBER = 99
        private const val BACKOFF_MILLIS = 2_000L

        // 409: another offset; 400: the chunk broke off, part of it arrived.
        private val RESUMABLE = setOf(400, 409)

        // The Pi's gateway answers these while the server restarts.
        private val GATEWAY = setOf(502, 503, 504)
    }
}

/** target for number 1, then "a (2).jpg", "a (3).jpg" and so on. */
fun numbered(target: String, number: Int): String {
    if (number <= 1) return target
    val slash = target.lastIndexOf('/')
    val name = target.substring(slash + 1)
    val dot = name.lastIndexOf('.')
    val numberedName = if (dot > 0) {
        "${name.substring(0, dot)} ($number)${name.substring(dot)}"
    } else {
        "$name ($number)"
    }
    return target.substring(0, slash + 1) + numberedName
}

// InputStream.skipNBytes needs Java 12, which older Android versions lack.
private fun InputStream.skipNBytesCompat(count: Long) {
    var left = count
    while (left > 0) {
        val skipped = skip(left)
        if (skipped > 0) {
            left -= skipped
        } else if (read() < 0) {
            throw EOFException()
        } else {
            left--
        }
    }
}
