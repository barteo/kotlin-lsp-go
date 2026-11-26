package demo

fun chain(items: List<Int>) = items
.filter { it > 0 }
.map {
it * 2
}
    .sum()

fun ops(a: Boolean, b: Boolean, c: Boolean): Boolean {
    val x =
    a &&
    b ||
    c
    val y = a &&
            b
    return x ||
    y
}

fun calls() {
    foo(
    1,
    2,
    )
    val z = listOf(
            1,
            2
            )
    bar(baz(
    3
    ))
    val s = items
    ?: return
    val r = if (z.isEmpty())
    1
    else
    2
    val t = run {
    listOf(1)
    }.map {
    it
    }
}

class Header(
val a: Int,
val b: Int,
) : Base(),
Other {
    val p: Int
    get() = 1

    override fun equals(other: Any?): Boolean =
        other is Header && (a == other.a ||
                b == other.b)
}

interface Bounded<T>
where T : Comparable<T> {
    /**
* Doc.
       * More.
     */
    fun x(): T
}
