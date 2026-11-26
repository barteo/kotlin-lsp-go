package zoo

abstract class /*|*/Animal(val /*|*/name: String) {
    abstract fun /*|*/sound(): String
    open fun describe() = "$name says ${sound()}"
}

open class Dog(name: String) : Animal(name) {
    constructor() : this("dog")
    override fun sound() = "woof"
}

class Puppy : Dog() {
    override fun /*|*/sound() = "yip"
}

object Cat : Animal(name = "cat") {
    override fun sound() = "meow"
}

fun /*|*/greet(animal: Animal, /*|*/loud: Boolean = false): String {
    val s = animal.describe()
    return if (loud) s.uppercase() else s
}
