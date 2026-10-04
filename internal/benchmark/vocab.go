package benchmark

import "strings"

// vocabularyText is the raw word list of the synthetic texts. It only holds content words
// (no Spanish stop words), so that any query built from it produces a non-empty tsquery.
// Topics: training, car, work, health and daily notes.
const vocabularyText = `
entrenamiento rutina sentadilla press banca peso muerto series repeticiones descanso cardio
carrera kilómetros ritmo zancada bicicleta natación piscina gimnasio mancuernas barra dominadas
flexiones abdominales estiramientos calentamiento movilidad resistencia fuerza hipertrofia
volumen intensidad progresión lesión recuperación maratón pulsaciones cinta cuerda remo
zapatillas objetivo récord sesión entrenador correr entrenar levantar nadar pedalear estirar

coche motor aceite filtro neumáticos presión frenos pastillas discos batería revisión seguro
gasolina diésel depósito consumo embrague cambio marcha volante suspensión amortiguadores
alineación correa distribución bujías radiador refrigerante limpiaparabrisas faros luces
matrícula taller mecánico garaje aparcamiento atasco autopista peaje kilometraje avería grúa
repuesto factura presupuesto ruedas maletero carrocería reparar arrancar frenar aparcar itv

trabajo reunión proyecto cliente proveedor contrato informe entrega plazo tarea equipo jefe
compañero oficina teletrabajo horario nómina vacaciones ascenso estrategia producto lanzamiento
correo llamada agenda calendario prioridad despliegue servidor datos código error incidencia
solicitud documentación presentación propuesta negociación ventas facturación contabilidad
auditoría formación curso entrevista candidato empresa sucursal licencia informática red
seguridad copia migración programar enviar planificar terminar empezar

salud médico cita análisis sangre tensión colesterol glucosa receta medicamento dosis alergia
dolor cabeza espalda rodilla hombro fisioterapeuta masaje dieta proteínas hidratos grasas
calorías fruta verdura agua sueño insomnio estrés ansiedad meditación respiración vitamina
hierro vacuna dentista oftalmólogo gafas resfriado fiebre tos gripe chequeo urgencias hospital
analítica dermatólogo descansar dormir

desayuno comida cena compra supermercado cocina limpieza lavadora casa alquiler hipoteca recibo
luz gas internet teléfono paquete cartero banco tarjeta transferencia ahorro gasto viaje vuelo
hotel maleta playa montaña senderismo cumpleaños regalo amigos familia hermano padres abuela
perro paseo veterinario película serie libro lectura música concierto café recado pendiente
recordatorio idea plan fiesta boda comprar pagar recordar preparar reservar cancelar aplazar
anotar

importante urgente nuevo viejo rápido lento largo corto pesado ligero cansado contento difícil
fácil caro barato mejorar cambiar revisar llamar
`

// vocabulary is the deduplicated word list, in the order of vocabularyText.
var vocabulary = buildVocabulary(vocabularyText)

// buildVocabulary splits text into words and drops repeated ones, keeping the first occurrence.
func buildVocabulary(text string) []string {
	seen := map[string]bool{}
	var words []string
	for _, w := range strings.Fields(text) {
		if !seen[w] {
			seen[w] = true
			words = append(words, w)
		}
	}
	return words
}

// embedPhrases are the fixed, distinct Spanish queries used to measure the real embedding
// latency. They read like what a person would ask a memory.
var embedPhrases = []string{
	"qué rutina de entrenamiento sigo los lunes",
	"cuándo fue la última revisión del coche",
	"qué dijo el médico sobre mi tensión",
	"cuál es el presupuesto del proyecto con el cliente",
	"a qué hora entreno normalmente por la mañana",
	"qué peso levanté en la última sesión de sentadilla",
	"cuándo caduca el seguro del coche",
	"qué medicamento tengo que tomar por la noche",
	"qué acordamos en la reunión de ayer con el equipo",
	"cuánto pago de alquiler cada mes",
	"cuál es mi objetivo para la próxima maratón",
	"qué neumáticos lleva el coche ahora mismo",
	"dónde aparqué el coche la última vez en el aeropuerto",
	"qué alergias tengo que recordar al médico",
	"cuál es la contraseña del wifi de casa de mis padres",
	"qué libro estaba leyendo antes de las vacaciones",
	"cuándo es el cumpleaños de mi hermano",
	"qué plan de dieta me recomendó el nutricionista",
	"cómo se llama el mecánico de confianza del barrio",
	"qué tareas tengo pendientes para la entrega del viernes",
	"cuántos kilómetros corrí la semana pasada",
	"qué decidimos sobre la migración del servidor",
	"qué película me recomendó mi amigo el otro día",
	"cuándo tengo cita con el dentista",
	"qué aceite necesita el motor del coche",
	"qué ejercicios de espalda me mandó el fisioterapeuta",
	"cuál fue el error que apareció en el despliegue",
	"qué comida prefiere mi abuela para las celebraciones",
	"cuánto cuesta la cuota del gimnasio",
	"qué vuelo reservé para el viaje de verano",
	"cuál es el horario del teletrabajo esta semana",
	"qué hotel elegimos para la boda de mi prima",
	"dónde guardé la factura de la lavadora",
	"qué análisis de sangre me tengo que repetir",
	"cuál es la presión recomendada de las ruedas",
	"qué ideas anoté para el lanzamiento del producto",
	"cómo se llama el veterinario de mi perro",
	"qué gasto de la tarjeta no reconozco este mes",
	"cuándo toca cambiar las pastillas de freno",
	"qué propuso el proveedor en la negociación del contrato",
}
