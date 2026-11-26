package demo

// Its result type is inferred, from another file than the completion's.
fun makeBox() = Box(2)

val boxed = makeBox()
