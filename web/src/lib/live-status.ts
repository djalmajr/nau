// Status da conexão em tempo real. Nesta base o hub ainda não expõe o canal
// ao vivo, então o hook sempre devolve "offline"; o tempo real chega num card
// seguinte e passa a ser alimentado aqui.
export type LiveStatus = "live" | "reconnecting" | "offline";

export function useLiveStatus(): LiveStatus {
  return "offline";
}
