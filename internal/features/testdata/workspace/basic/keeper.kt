package keeper

import zoo.Animal
import zoo.Dog
import zoo.greet

class Keeper(private val animals: List<Animal>) {
    fun /*|*/feedAll() {
        for (a in animals) {
            println(/*|*/greet(a, loud = true))
        }
        greet(/*|*/Dog())
    }
}

fun /*|*/main() {
    val /*|*/k = Keeper(listOf(Dog("rex")))
    k./*|*/feedAll()
    k.feedAll()
    println(k[0].name)
    zoo.Cat.sound()
}

operator fun Keeper.get(i: Int): Animal = Dog()
