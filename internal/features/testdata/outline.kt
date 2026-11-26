/**
 * A KDoc comment
 * over lines.
 */
@file:JvmName("Outline")
package a.b

import kotlin.collections.List
import java.io.File as JFile
import kotlin.math.*

const val MAX = 10
val String.shout: String get() = uppercase()

@Deprecated("use D")
class C<T : Any>(val p: Int, var q: String, r: Long) : Base(), Iface {
    constructor(s: String) : this(1, s, 2L)

    fun f(x: a.B<T>, vararg ys: Int): List<T> = loop@ for (i in 0..1) { break@loop }

    val s = "a${p}b$p\n"

    companion object {
        fun create(): C<String> = C(1, "", 2)
    }

    // one
    // two
    // three
}

interface Iface
annotation class Ann
object Single { val x = 1 }

enum class E { A, B { override fun toString() = "b" } }

typealias Names = List<String>

fun <R> String.ext(block: (String) -> R): R {
    val (a, b) = Pair(1, 2)
    val local = h(name = 1) { it }
    when (a) {
        1 -> {}
        else -> {}
    }
    return block(this)
}

val raw = """
    multi
    line
"""
