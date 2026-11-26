package shapes

/**
 * A shape with an [area].
 *
 * @property name what the shape is called
 */
interface Shape {
    val name: String

    /** The area, in square units. @return never negative */
    fun area(): Double
}

/** A circle of radius [r]. */
data class Circle(val r: Double) : Shape {
    override val name = "circle"
    override fun area() = 3.14159 * r * r
}

class Square(private val side: Double) : Shape {
    override val name: String get() = "square"
    override fun area(): Double = side * side
}

fun <T : Shape> largest(shapes: List<T>): T? = shapes.maxByOrNull { it.area() }
