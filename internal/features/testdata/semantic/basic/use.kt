package app

import shapes.Circle
import shapes.Shape
import shapes.largest

fun main() {
    val /*|*/circles = listOf(/*|*/Circle(1.0), Circle(2.0))
    var total = 0.0
    for (c in circles) {
        /*|*/total += c./*|*/area()
    }
    val big: /*|*/Shape? = /*|*/largest(circles)
    println(/*|*/big?.name + /*|*/total)
    /*|*/undefined(total)
    big?./*|*/nope()
    val n = 1 as Int
}
