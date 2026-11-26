package   demo
import kotlin.math.max
class   Box<T:Any>(val value:T,private val label:String="box"):Comparable<Box<T>>,Cloneable{
  override fun compareTo(other:Box<T>):Int{
    return label.compareTo( other.label )
  }
    fun   describe( ) = "$label: ${value}"



    val size:Int get()=label.length
}

fun   main(args:Array<String>){
val list=listOf(1,2,3)
  val doubled=list.map{it*2}.filter{ it>2 }
  for(i in 0..<list.size){println(list[i])}
  if(doubled.isEmpty()){
  println("empty")
  }else if (doubled.size>2) println("big")
  else{
      println( - doubled.size )
  }
  when(val n=doubled.size){
  0->println("none")
      1,2 ->{
println("few")
      }
    else->println(n!!)
  }
  val nullable:String?=null
  println(nullable?.length?:0)
  println(Box::class.simpleName)
  loop@for(x in list){if(x>1)break@loop}
  try{max(1,2)}catch(e:Exception){throw IllegalStateException(e)}finally{println()}
  val f:(Int)->Int={x->x+1}
  println(f(1) as Int)
  println(list is List<*>)
}
