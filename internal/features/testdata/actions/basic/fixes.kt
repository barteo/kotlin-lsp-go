package fixes

import kotlin.math.min
import kotlin.collections.ArrayList
import kotlin.math.abs
import kotlin.math.abs

enum class Color { RED, GREEN, BLUE }

sealed class Shape {
    object Dot : Shape()
    class Circle(val r: Double) : Shape()
    class Square(val side: Double) : Shape()
}

interface Named {
    val name: String
    fun greet(other: String): String
}

class Person : Named {
}

object Nobody : Named

fun describe(c: Color): String = when (c) {
    Color.RED -> "red"
}

fun area(s: Shape): Double = when (s) {
    is Shape.Circle -> 3.14 * s.r * s.r
}

fun unused(text: String?) {
    val never = 42
    val alsoNever = text?.length
    val notNull = "x"
    println(notNull?.length)
    println(notNull as String)
    val t = measureTime { }
    println(abs(-1) + t.inWholeSeconds)
    listOf(1).forEach { x -> println() }
}

public fun visible(width: Int, height: Int) = width * height

fun calls() {
    val v = visible(1, 2)
    println(v)
    val r = kotlin.random.Random.Default
    println(r.nextInt(0, until = 10))
}
