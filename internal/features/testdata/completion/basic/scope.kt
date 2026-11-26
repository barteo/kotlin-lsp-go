package demo

import kotlin.math.max

/** A box of a value. */
class Box<T>(val value: T) {
    /** Returns the value. */
    fun get(): T = value
    fun map(f: (T) -> T): Box<T> = Box(f(value))
}

fun topLevel(count: Int, name: String): Int {
    val local = Box("x")
    local.va/*|*/
    lo/*|*/
    topL/*|*/
    max(count, co/*|*/)
    topLevel(1, na/*|*/)
    topLevel(count = 1, /*|*/)
    val s: Bo/*|*/
    measureT/*|*/
    "text".upp/*|*/
    Hel/*|*/
    otherF/*|*/
    // a comment lo/*|*/
    val t = "in a string lo/*|*/"
    Box(/*|*/)
    listOf(1, /*|*/2)
    local.map(/*|*/)
    local.m/*|*/
    makeBox().g/*|*/
    boxed.va/*|*/
    if/*|*/
    return 0
}

ma/*|*/

abstract class Animal {
    abstract fun sound(): String
    open val legs: Int get() = 4
    fun final() {}
}

class Dog : Animal() {
    override fun sound() = "woof"
    /*|*/
    override fun /*|*/
    le/*|*/
    toS/*|*/
}

@Dep/*|*/
fun end(b: Box<Int>) {
    b./*|*/
}
