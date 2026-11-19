import Foundation

/// Run only with the Go-generated fictional TestQuotaSummaryAndNativeFixture.
@main struct QuotaSmoke {
 static func main() throws {
  guard CommandLine.arguments.count==2 else { fatalError("Fictional quota JSON path required") }
  let data=try Data(contentsOf:URL(fileURLWithPath:CommandLine.arguments[1]))
  let list=try Contract.panelList(data,exit:0)
  guard let q=list.accountQuotas,q.accounts.map(\.label).prefix(2)==["Example Codex","Example Claude"] else {fatalError("Not the fictional quota fixture")}
  let model=MenuBuilder.build(.read(list,at:Date()),now:Date())
  guard let account=model.rows.first(where:{$0.title=="Example Codex"}),account.detail.contains("37.5"),account.submenu.contains(where:{$0.accessibilityLabel.contains("currency not reported")}),account.submenu.compactMap(\.command)==[.openPanel] else {fatalError("Native quota menu lost units or authority boundary")}
  guard let claude=model.rows.first(where:{$0.title=="Example Claude"}),claude.detail=="Quota unavailable",!claude.submenu.contains(where:{$0.title.contains("0%")}) else {fatalError("Unsupported provider became zero")}
  var object=try JSONSerialization.jsonObject(with:data) as! [String:Any]
  var quotas=object["accountQuotas"] as! [String:Any]
  var accounts=quotas["accounts"] as! [[String:Any]]
  var summary=accounts[0]["summary"] as! [String:Any]
  var rows=summary["rows"] as! [[String:Any]]
  rows[0]["usedPercent"]=101;summary["rows"]=rows;accounts[0]["summary"]=summary;quotas["accounts"]=accounts;object["accountQuotas"]=quotas
  do{_=try Contract.panelList(JSONSerialization.data(withJSONObject:object),exit:0);fatalError("Invalid percentage accepted")}catch{}
  object.removeValue(forKey:"accountQuotas")
  guard try Contract.panelList(JSONSerialization.data(withJSONObject:object),exit:0).accountQuotas==nil else {fatalError("Old CLI fallback lost")}
  guard Contract.text(String(repeating:"a",count:200)).count==180,Contract.panelAuthority(String(repeating:"a",count:231)) != nil else {fatalError("Generic or authority bound changed")}
  print("Native saved quota fixture passed: named windows/units, unsupported Claude, strict percentage, old CLI, no refresh authority; generic180 and full disclosure preserved")
 }
}
