package hints

class Box<T>(val value: T)

fun area(width: Int, height: Int) = width * height

fun greet(name: String, loud: Boolean = false): String = if (loud) name.uppercase() else name

val shown = area(2, 3)
val obvious = Box(1)
val literal = "text"
val cast = shown as Number

fun main() {
    val total = area(4, height = 5)
    val (a, b) = Pair(1, "two")
    for (x in listOf(1, 2)) {
        println(x)
    }
    val names = listOf("a", "b").map { it.length }
    val built = buildString { append(total) }
    greet("Ann", true)
    greet(literal, loud = false)
    val width = 3
    area(width, -1)
    println(names + built + a + b + obvious + cast)
}
