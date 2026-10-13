import ts from 'typescript'
import { readdirSync, readFileSync } from 'node:fs'
import { resolve, relative } from 'node:path'

const root=resolve(process.argv[2]), routes=JSON.parse(readFileSync(process.argv[3],'utf8'))
const files=[]
function walk(dir){for(const e of readdirSync(dir,{withFileTypes:true})){const p=resolve(dir,e.name);if(e.isDirectory())walk(p);else if(/\.tsx?$/.test(p))files.push(p)}}
walk(resolve(root,'src'))
const program=ts.createProgram(files,{jsx:ts.JsxEmit.ReactJSX,target:ts.ScriptTarget.ESNext,module:ts.ModuleKind.ESNext,skipLibCheck:true,noEmit:true})
const checker=program.getTypeChecker(), contracts=new Map(), generic=new Set()
function values(node,seen=new Set()){
 const literal=text=>[{text,choices:{}}]
 if(!node||seen.has(node))return literal('*');const next=new Set(seen);next.add(node)
 if(ts.isStringLiteralLike(node))return literal(node.text)
 if(ts.isParenthesizedExpression(node))return values(node.expression,next)
 if(ts.isConditionalExpression(node))return [true,false].flatMap(branch=>values(branch?node.whenTrue:node.whenFalse,next).filter(v=>v.choices[node.condition.getText()]===undefined||v.choices[node.condition.getText()]===branch).map(v=>({text:v.text,choices:{...v.choices,[node.condition.getText()]:branch}})))
 const combine=(a,b)=>a.flatMap(x=>b.filter(y=>Object.keys(x.choices).every(k=>y.choices[k]===undefined||y.choices[k]===x.choices[k])).map(y=>({text:x.text+y.text,choices:{...x.choices,...y.choices}}))).slice(0,64)
 if(ts.isBinaryExpression(node)&&node.operatorToken.kind===ts.SyntaxKind.PlusToken)return combine(values(node.left,next),values(node.right,next))
 if(ts.isTemplateExpression(node)){let parts=literal(node.head.text);for(const span of node.templateSpans)parts=combine(parts,values(span.expression,next).map(s=>({...s,text:s.text+span.literal.text})));return parts}
 if(ts.isIdentifier(node)){const d=checker.getSymbolAtLocation(node)?.valueDeclaration;if(d&&ts.isVariableDeclaration(d)&&d.initializer)return values(d.initializer,next)}
 return literal('*')
}
function add(node,method,expression){
 const paths=values(expression).map(v=>v.text)
 for(let path of paths){
  path=path.split('?')[0]
  if(!path.startsWith('/api/')){if(path.includes('*'))generic.add(relative(root,node.getSourceFile().fileName)+':'+(node.getSourceFile().getLineAndCharacterOfPosition(node.getStart()).line+1));continue}
  const key=method+' '+path
  contracts.set(key,{method,path,file:relative(root,node.getSourceFile().fileName),line:node.getSourceFile().getLineAndCharacterOfPosition(node.getStart()).line+1})
 }
}
for(const file of files){const source=program.getSourceFile(file);function visit(node){
 if(ts.isCallExpression(node)&&ts.isIdentifier(node.expression)){
  const name=node.expression.text
  if(['api','post','useResource','usePartnerData','useSession'].includes(name)&&node.arguments[0]){
   const method=name==='post'||name==='api'&&node.arguments.length>1&&node.arguments[1].getText()!=='undefined'?'POST':'GET'
   add(node,method,node.arguments[0])
  }
  if(name==='fetch'&&node.arguments[0]&&node.arguments[1]&&ts.isObjectLiteralExpression(node.arguments[1])){
   const property=node.arguments[1].properties.find(p=>ts.isPropertyAssignment(p)&&p.name.getText()==='method')
   const method=property?values(property.initializer)[0].text:'GET';if(method==='GET'||method==='POST')add(node,method,node.arguments[0])
  }
 }
 if(ts.isJsxAttribute(node)&&['endpoint','path'].includes(node.name.getText())&&node.initializer){
  const expression=ts.isJsxExpression(node.initializer)?node.initializer.expression:node.initializer
  add(node,'GET',expression)
 }
 ts.forEachChild(node,visit)
 }visit(source)}
const missing=[]
for(const c of contracts.values()){
 const parts=c.path.split('/')
 if(!routes.some(r=>r.method===c.method&&r.path.split('/').length===parts.length&&r.path.split('/').every((p,i)=>parts[i]==='*'||p.startsWith(':')||p===parts[i])))missing.push(c)
}
console.log(JSON.stringify({frontendContracts:contracts.size,registeredRoutes:routes.length,genericCallSites:generic.size,missing},null,2))
if(missing.length||contracts.size<60)process.exitCode=1
